package gql

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/jinzhu/inflection"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

//go:embed assets/tpl/*
var templates embed.FS

//go:embed assets/sql/pgsql.sql
var pgsql string

func init() {
	inflection.AddUncountable("children")
}

// Config 表示GraphQL配置
type Config struct {
	std.Config           `mapstructure:",squash"`
	internal.TableConfig `mapstructure:"schema"`
}

// Metadata 表示GraphQL元数据
type Metadata struct {
	v   *viper.Viper
	db  *gorm.DB
	cfg *Config
	tpl *template.Template

	// 主索引 - 类名 -> 类定义
	Nodes utl.AnyMap[*internal.Class]

	// 辅助索引 - 表名 -> 类名
	tableToClass map[string]string

	// 辅助索引 - 原始表名 -> 类名
	rawTableToClass map[string]string

	// 外键关系缓存
	relationships map[string]map[string]*internal.ForeignKey
}

// MetadataCache 元数据缓存结构
type MetadataCache struct {
	Classes       []*internal.Class                          `json:"classes"`
	Relationships map[string]map[string]*internal.ForeignKey `json:"relationships"`
}

// NewMetadata 创建一个新的元数据处理器
func NewMetadata(v *viper.Viper, d *gorm.DB) (*Metadata, error) {
	//初始化模板
	tpl, err := template.ParseFS(templates, "assets/tpl/*.tpl")
	if err != nil {
		return nil, err
	}

	//初始化配置
	cfg := &Config{TableConfig: internal.TableConfig{Mapping: dataTypes}}
	v.SetDefault("schema.default-limit", 10)
	v.SetDefault("schema.source", internal.SourceDatabase)
	v.SetDefault("schema.enable-camel-case", true)
	v.SetDefault("schema.enable-cache", false)
	v.SetDefault("schema.cache-path", "./metadata_cache.json")
	v.SetDefault("schema.schema", "public")
	v.SetDefault("schema.table-prefix", []string{})

	if err = v.Unmarshal(cfg); err != nil {
		return nil, err
	}

	my := &Metadata{
		v:               v,
		db:              d,
		cfg:             cfg,
		tpl:             tpl,
		Nodes:           make(utl.AnyMap[*internal.Class]),
		tableToClass:    make(map[string]string),
		rawTableToClass: make(map[string]string),
		relationships:   make(map[string]map[string]*internal.ForeignKey),
	}

	// 加载元数据
	if err := my.loadMetadata(); err != nil {
		return nil, err
	}

	// 加载加载其他选项
	for _, o := range []internal.LoadOption{
		my.expressions,
		my.tableOption,
		my.orderOption,
		my.whereOption,
		my.inputOption,
		my.entryOption,
	} {
		if err := o(); err != nil {
			return nil, err
		}
	}

	return my, nil
}

// loadMetadata 加载元数据
func (my *Metadata) loadMetadata() error {
	log.Info().Msg("开始加载元数据")

	// 初始化数据结构
	my.Nodes = make(map[string]*internal.Class)
	my.tableToClass = make(map[string]string)
	my.rawTableToClass = make(map[string]string)
	my.relationships = make(map[string]map[string]*internal.ForeignKey)

	// 根据环境决定加载方式
	switch my.cfg.Source {
	case internal.SourceDatabase:
		// 开发环境：从数据库加载
		log.Info().Msg("从数据库加载元数据")
		if err := my.loadFromDatabase(); err != nil {
			log.Error().Err(err).Msg("从数据库加载元数据失败")
			return fmt.Errorf("加载数据库元数据失败: %w", err)
		}

		// 如果启用了缓存，保存到文件
		if my.cfg.EnableCache {
			log.Info().Str("cache", my.cfg.CachePath).Msg("保存元数据到缓存文件")
			if err := my.saveMetadataToFile(my.cfg.CachePath); err != nil {
				log.Error().Err(err).Str("cache", my.cfg.CachePath).Msg("保存元数据到缓存文件失败")
				return fmt.Errorf("保存元数据缓存失败: %w", err)
			}
		}

	case internal.SourceFile:
		// 生产环境：从预设文件加载
		log.Info().Str("file", my.cfg.CachePath).Msg("从预设文件加载元数据")
		if err := my.loadMetadataFromFile(my.cfg.CachePath); err != nil {
			log.Error().Err(err).Str("file", my.cfg.CachePath).Msg("从预设文件加载元数据失败")
			return fmt.Errorf("从文件加载元数据失败: %w", err)
		}

	case internal.SourceConfig:
		// 配置环境：从配置加载
		log.Info().Msg("从配置加载元数据")
		if err := my.loadFromConfig(); err != nil {
			log.Error().Err(err).Msg("从配置加载元数据失败")
			return fmt.Errorf("从配置加载元数据失败: %w", err)
		}

	default:
		log.Error().Str("source", string(my.cfg.Source)).Msg("未知的元数据加载来源")
		return fmt.Errorf("未知的元数据加载来源: %s", my.cfg.Source)
	}

	// 合并配置的元数据 - 从数据库或文件加载后，尝试从配置合并额外定义
	if my.cfg.Source != internal.SourceConfig {
		// 尝试从配置加载额外元数据并合并
		log.Info().Msg("尝试从配置合并额外元数据")
		if err := my.loadFromConfig(); err != nil {
			// 记录错误但不中断流程
			log.Warn().Err(err).Msg("合并配置元数据失败，继续使用现有元数据")
		}
	}

	log.Info().
		Int("classes", len(my.Nodes)).
		Int("relationships", len(my.relationships)).
		Msg("元数据加载完成")
	log.Debug().Msg("元数据加载过程结束")
	return nil
}

// mergeMetadata 合并元数据
func (my *Metadata) mergeMetadata(classes map[string]*internal.Class, relationships map[string]map[string]*internal.ForeignKey) {
	log.Info().Msg("开始合并元数据")
	mergedClasses := 0
	mergedRelations := 0

	// 合并类
	for tableName, class := range classes {
		// 检查是否应包含此表
		if !my.shouldIncludeTable(tableName) {
			log.Debug().Str("table", tableName).Msg("排除表")
			continue
		}

		// 转换类名
		className := my.convertTableName(tableName)
		if className != tableName {
			log.Debug().Str("table", tableName).Str("class", className).Msg("表名转换")
		}

		// 更新类名
		class.Name = className

		// 处理字段
		filteredFields := 0
		renamedFields := 0
		for columnName, field := range class.Fields {
			// 检查是否应包含此字段
			if !my.shouldIncludeField(columnName) {
				delete(class.Fields, columnName)
				filteredFields++
				continue
			}

			// 转换字段名
			fieldName := my.convertFieldName(tableName, columnName)

			// 如果字段名发生了变化，需要更新
			if fieldName != columnName {
				// 创建新的条目
				class.Fields[fieldName] = field
				field.Name = fieldName

				// 删除旧的条目
				delete(class.Fields, columnName)
				renamedFields++
			}
		}

		if filteredFields > 0 || renamedFields > 0 {
			log.Debug().Str("table", tableName).Int("filtered", filteredFields).Int("renamed", renamedFields).Msg("字段处理")
		}

		// 检查类是否已存在
		if existingClass, ok := my.Nodes[className]; ok {
			// 合并字段
			mergedFields := 0
			for fieldName, field := range class.Fields {
				if _, exists := existingClass.Fields[fieldName]; !exists {
					existingClass.Fields[fieldName] = field
					mergedFields++
				}
			}

			if mergedFields > 0 {
				log.Debug().Str("class", className).Int("fields", mergedFields).Msg("合并字段到现有类")
			}
		} else {
			// 添加新类
			my.Nodes[className] = class
			my.tableToClass[class.Table] = className
			if class.Table != tableName {
				my.rawTableToClass[tableName] = className
			}
			mergedClasses++
			log.Debug().Str("class", className).Int("fields", len(class.Fields)).Msg("添加新类")
		}
	}

	// 合并关系
	for tableName, relations := range relationships {
		// 检查是否应包含此表
		if !my.shouldIncludeTable(tableName) {
			continue
		}

		// 转换表名
		className := my.convertTableName(tableName)

		// 获取或创建关系映射
		tableRelations, exists := my.relationships[className]
		if !exists {
			tableRelations = make(map[string]*internal.ForeignKey)
			my.relationships[className] = tableRelations
		}

		// 处理表的所有关系
		for columnName, fk := range relations {
			// 检查是否应包含此字段
			if !my.shouldIncludeField(columnName) {
				continue
			}

			// 转换字段名
			fieldName := my.convertFieldName(tableName, columnName)

			// 转换目标表名和字段名
			targetClassName := my.convertTableName(fk.TableName)
			targetFieldName := my.convertFieldName(fk.TableName, fk.ColumnName)

			// 更新外键信息
			fk.TableName = targetClassName
			fk.ColumnName = targetFieldName

			// 添加到关系映射
			tableRelations[fieldName] = fk
			mergedRelations++

			log.Debug().
				Str("source", className).
				Str("field", fieldName).
				Str("target", targetClassName).
				Str("target_field", targetFieldName).
				Msg("添加关系")
		}
	}

	log.Info().
		Int("classes", mergedClasses).
		Int("relations", mergedRelations).
		Msg("元数据合并完成")
}

// loadFromDatabase 从数据库加载元数据
func (my *Metadata) loadFromDatabase() error {
	log.Info().Msg("开始从数据库加载元数据")

	// 创建数据库加载器
	loader, err := metadata.NewDatabaseLoader(my.db, my.cfg.Schema)
	if err != nil {
		log.Error().Err(err).Msg("创建数据库加载器失败")
		return err
	}
	log.Debug().Str("schema", my.cfg.Schema).Msg("创建数据库加载器")

	// 加载元数据
	log.Debug().Msg("开始从数据库加载元数据")
	classes, relationships, err := loader.LoadMetadata()
	if err != nil {
		log.Error().Err(err).Msg("数据库加载元数据失败")
		return err
	}
	log.Debug().Int("tables", len(classes)).Int("relations", len(relationships)).Msg("数据库元数据加载完成")

	// 处理命名转换和过滤
	log.Debug().Msg("开始处理元数据命名转换和过滤")
	for tableName, class := range classes {
		// 检查是否应包含此表
		if !my.shouldIncludeTable(tableName) {
			log.Debug().Str("table", tableName).Msg("排除表")
			continue
		}

		// 转换类名
		className := my.convertTableName(tableName)
		if className != tableName {
			log.Debug().Str("table", tableName).Str("class", className).Msg("表名转换")
		}

		// 更新类名
		class.Name = className

		// 处理字段
		filteredFields := 0
		renamedFields := 0
		for columnName, field := range class.Fields {
			// 检查是否应包含此字段
			if !my.shouldIncludeField(columnName) {
				delete(class.Fields, columnName)
				filteredFields++
				continue
			}

			// 转换字段名
			fieldName := my.convertFieldName(tableName, columnName)

			// 如果字段名发生了变化，需要更新
			if fieldName != columnName {
				// 创建新的条目
				class.Fields[fieldName] = field
				field.Name = fieldName

				// 删除旧的条目
				delete(class.Fields, columnName)
				renamedFields++
			}
		}

		if filteredFields > 0 || renamedFields > 0 {
			log.Debug().Str("table", tableName).Int("filtered", filteredFields).Int("renamed", renamedFields).Msg("字段处理")
		}

		// 更新主键列表（转换字段名）
		for i, pkName := range class.PrimaryKeys {
			class.PrimaryKeys[i] = my.convertFieldName(tableName, pkName)
		}

		// 添加到Nodes集合
		my.Nodes[className] = class

		// 添加到表名索引
		my.tableToClass[class.Table] = className

		// 添加到原始表名索引（如果名称转换后不同）
		if class.Table != tableName {
			my.rawTableToClass[tableName] = className
		}
	}

	// 处理关系
	processedRelations := 0
	for tableName, relations := range relationships {
		// 检查是否应包含此表
		if !my.shouldIncludeTable(tableName) {
			continue
		}

		// 转换表名
		className := my.convertTableName(tableName)

		// 创建关系映射
		tableRelations := make(map[string]*internal.ForeignKey)

		// 处理表的所有关系
		for columnName, fk := range relations {
			// 检查是否应包含此字段
			if !my.shouldIncludeField(columnName) {
				continue
			}

			// 转换字段名
			fieldName := my.convertFieldName(tableName, columnName)

			// 转换目标表名和字段名
			targetClassName := my.convertTableName(fk.TableName)
			targetFieldName := my.convertFieldName(fk.TableName, fk.ColumnName)

			// 更新外键信息
			fk.TableName = targetClassName
			fk.ColumnName = targetFieldName

			// 添加到关系映射
			tableRelations[fieldName] = fk
			processedRelations++

			log.Debug().
				Str("source_table", className).
				Str("source_field", fieldName).
				Str("target_table", targetClassName).
				Str("target_field", targetFieldName).
				Str("kind", string(fk.Kind)).
				Msg("关系映射")
		}

		// 添加到全局关系映射
		if len(tableRelations) > 0 {
			my.relationships[className] = tableRelations
		}
	}

	log.Info().
		Int("tables", len(my.Nodes)).
		Int("relations", processedRelations).
		Msg("数据库元数据处理完成")
	return nil
}

// loadMetadataFromFile 从文件加载元数据
func (my *Metadata) loadMetadataFromFile(filePath string) error {
	log.Info().Str("file", filePath).Msg("开始从文件加载元数据")

	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("读取元数据文件失败")
		return err
	}

	var metadataCache struct {
		Nodes           map[string]*internal.Class
		TableToClass    map[string]string
		RawTableToClass map[string]string
		Relationships   map[string]map[string]*internal.ForeignKey
	}

	if err := json.Unmarshal(data, &metadataCache); err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("解析元数据JSON失败")
		return err
	}

	my.Nodes = metadataCache.Nodes
	my.tableToClass = metadataCache.TableToClass
	my.rawTableToClass = metadataCache.RawTableToClass
	my.relationships = metadataCache.Relationships

	log.Info().
		Int("tables", len(my.Nodes)).
		Int("relations", len(my.relationships)).
		Str("file", filePath).
		Msg("文件元数据加载完成")
	return nil
}

// saveMetadataToFile 保存元数据到文件
func (my *Metadata) saveMetadataToFile(filePath string) error {
	log.Info().Str("file", filePath).Msg("开始保存元数据到文件")

	// 创建要保存的数据结构
	metadataCache := struct {
		Nodes           map[string]*internal.Class
		TableToClass    map[string]string
		RawTableToClass map[string]string
		Relationships   map[string]map[string]*internal.ForeignKey
	}{
		Nodes:           my.Nodes,
		TableToClass:    my.tableToClass,
		RawTableToClass: my.rawTableToClass,
		Relationships:   my.relationships,
	}

	// 转换为JSON
	data, err := json.MarshalIndent(metadataCache, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("元数据序列化为JSON失败")
		return err
	}

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("创建目录失败")
		return err
	}

	// 写入文件
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("写入元数据文件失败")
		return err
	}

	log.Info().
		Int("tables", len(my.Nodes)).
		Int("relationships", len(my.relationships)).
		Str("file", filePath).
		Int("size", len(data)).
		Msg("元数据缓存已保存到文件")
	return nil
}

// 名称转换相关方法

// convertTableName 转换表名
func (my *Metadata) convertTableName(rawName string) string {
	// 去除前缀
	name := rawName

	// 检查所有前缀
	for _, prefix := range my.cfg.TablePrefix {
		if prefix != "" && strings.HasPrefix(name, prefix) {
			name = name[len(prefix):]
			break // 一旦找到匹配的前缀就停止
		}
	}

	// 应用自定义映射
	if mappedName, ok := my.cfg.TableMapping[name]; ok {
		return mappedName
	}

	// 转换为驼峰命名
	if my.cfg.EnableCamelCase {
		name = strcase.ToCamel(name)
	}

	return name
}

// convertFieldName 转换字段名
func (my *Metadata) convertFieldName(tableName, rawName string) string {
	// 应用自定义映射
	key := tableName + "." + rawName
	if mappedName, ok := my.cfg.FieldMapping[key]; ok {
		return mappedName
	}
	if mappedName, ok := my.cfg.FieldMapping[rawName]; ok {
		return mappedName
	}

	// 转换为驼峰命名
	name := rawName
	if my.cfg.EnableCamelCase {
		name = strcase.ToCamel(name)
	}

	return name
}

// shouldIncludeTable 检查是否应该包含表
func (my *Metadata) shouldIncludeTable(tableName string) bool {
	// 检查排除列表
	for _, excluded := range my.cfg.ExcludeTables {
		if excluded == tableName {
			return false
		}
	}

	// 检查包含列表
	if len(my.cfg.IncludeTables) > 0 {
		for _, included := range my.cfg.IncludeTables {
			if included == tableName {
				return true
			}
		}
		return false
	}

	return true
}

// shouldIncludeField 检查是否应该包含字段
func (my *Metadata) shouldIncludeField(fieldName string) bool {
	// 检查排除列表
	for _, excluded := range my.cfg.ExcludeFields {
		if excluded == fieldName {
			return false
		}
	}

	return true
}

// Marshal 序列化元数据为GraphQL模式
func (my *Metadata) Marshal() (string, error) {
	var w strings.Builder
	if err := my.tpl.ExecuteTemplate(&w, "build.tpl", my.Nodes); err != nil {
		return "", err
	}
	return w.String(), nil
}

// FindClass 根据类名查找类
func (my *Metadata) FindClass(className string, virtual bool) (*internal.Class, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return nil, false
	}
	return class, true
}

// FindField 根据类名和字段名查找字段
func (my *Metadata) FindField(className, fieldName string, virtual bool) (*internal.Field, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return nil, false
	}
	field, ok := class.Fields[fieldName]
	if !ok || field.Virtual != virtual {
		return nil, false
	}
	return field, true
}

// FindClassByTable 根据表名查找类
func (my *Metadata) FindClassByTable(tableName string) (*internal.Class, bool) {
	className, ok := my.tableToClass[tableName]
	if !ok {
		// 尝试使用原始表名查找
		className, ok = my.rawTableToClass[tableName]
		if !ok {
			return nil, false
		}
	}

	return my.Nodes[className], true
}

// GetForeignKey 获取外键关系
func (my *Metadata) GetForeignKey(sourceTable, sourceColumn string) (*internal.ForeignKey, bool) {
	tableRels, ok := my.relationships[sourceTable]
	if !ok {
		return nil, false
	}

	fk, ok := tableRels[sourceColumn]
	return fk, ok
}

// TableName 获取类的表名
func (my *Metadata) TableName(className string, virtual bool) (string, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return "", false
	}
	return class.Table, len(class.Table) > 0
}

// ColumnName 获取字段的列名
func (my *Metadata) ColumnName(className, fieldName string, virtual bool) (string, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return "", false
	}
	field, ok := class.Fields[fieldName]
	if !ok || field.Virtual != virtual {
		return "", false
	}
	return field.Column, len(field.Column) > 0
}

// 以下是加载选项的实现

// expressions 加载表达式
func (my *Metadata) expressions() error {
	// TODO: 实现表达式加载
	return nil
}

// tableOption 加载表选项
func (my *Metadata) tableOption() error {
	// TODO: 实现表选项加载
	return nil
}

// orderOption 加载排序选项
func (my *Metadata) orderOption() error {
	// TODO: 实现排序选项加载
	return nil
}

// whereOption 加载过滤选项
func (my *Metadata) whereOption() error {
	// TODO: 实现过滤选项加载
	return nil
}

// inputOption 加载输入选项
func (my *Metadata) inputOption() error {
	// TODO: 实现输入选项加载
	return nil
}

// entryOption 加载入口选项
func (my *Metadata) entryOption() error {
	// TODO: 实现入口选项加载
	return nil
}

// loadFromConfig 从配置加载元数据
func (my *Metadata) loadFromConfig() error {
	log.Info().Msg("开始从配置加载元数据")

	// 创建配置加载器
	loader := metadata.NewConfigLoader(my.v)
	log.Debug().Msg("创建配置加载器")

	// 加载元数据
	classes, relationships, err := loader.LoadMetadata()
	if err != nil {
		log.Error().Err(err).Msg("配置加载元数据失败")
		return err
	}
	log.Debug().Int("classes", len(classes)).Int("relations", len(relationships)).Msg("配置元数据加载完成")

	// 使用合并函数处理元数据
	my.mergeMetadata(classes, relationships)

	return nil
}
