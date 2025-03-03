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
	strcase.ConfigureAcronym("ID", "Id")
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

	// 统一索引: 支持类名、表名、原始表名查找
	Nodes map[string]*internal.Class
}

// MetadataCache 元数据缓存结构
type MetadataCache struct {
	Nodes map[string]*internal.Class `json:"nodes"`
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
		v:     v,
		db:    d,
		cfg:   cfg,
		tpl:   tpl,
		Nodes: make(map[string]*internal.Class),
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

	// 从主要来源加载基础元数据
	switch my.cfg.Source {
	case internal.SourceDatabase:
		// 从数据库加载
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
		// 从预设文件加载
		log.Info().Str("file", my.cfg.CachePath).Msg("从预设文件加载元数据")
		if err := my.loadMetadataFromFile(my.cfg.CachePath); err != nil {
			log.Error().Err(err).Str("file", my.cfg.CachePath).Msg("从预设文件加载元数据失败")
			return fmt.Errorf("从文件加载元数据失败: %w", err)
		}

	default:
		log.Error().Str("source", string(my.cfg.Source)).Msg("未知的元数据加载来源")
		return fmt.Errorf("未知的元数据加载来源: %s", my.cfg.Source)
	}

	// 加载并合并配置中的元数据（具有更高优先级）
	log.Info().Msg("加载配置元数据并合并")
	if err := my.loadFromConfig(); err != nil {
		log.Error().Err(err).Msg("加载配置元数据失败")
		return fmt.Errorf("加载配置元数据失败: %w", err)
	}

	log.Info().
		Int("classes", len(my.Nodes)).
		Msg("元数据加载完成")
	return nil
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

	// 初始化Nodes
	my.Nodes = make(map[string]*internal.Class)

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

		// 初始化表名索引
		if class.TableNames == nil {
			class.TableNames = make(map[string]bool)
		}

		// 添加到Nodes集合(支持通过类名查找)
		my.Nodes[className] = class

		// 添加表名索引
		class.TableNames[class.Table] = false
		my.Nodes[class.Table] = class

		// 如果原始表名不同，添加原始表名索引
		if class.Table != tableName {
			class.TableNames[tableName] = true
			my.Nodes[tableName] = class
		}
	}

	// 处理关系
	for sourceTable, relations := range relationships {
		if !my.shouldIncludeTable(sourceTable) {
			continue
		}

		for sourceColumn, relation := range relations {
			if !my.shouldIncludeField(sourceColumn) {
				continue
			}

			// 获取源类和字段
			sourceClass := my.Nodes[my.convertTableName(sourceTable)]
			if sourceClass == nil {
				continue
			}

			sourceField := sourceClass.GetField(my.convertFieldName(sourceTable, sourceColumn))
			if sourceField == nil {
				// 创建关系字段
				sourceField = &internal.Field{
					Name:    my.convertFieldName(sourceTable, sourceColumn),
					Column:  sourceColumn,
					Type:    my.convertTableName(relation.TargetClass),
					Virtual: true,
				}
				sourceClass.AddField(sourceField)
			}

			// 设置字段的关系引用
			sourceField.Relation = relation
		}
	}

	return nil
}

// loadMetadataFromFile 从文件加载元数据
func (my *Metadata) loadMetadataFromFile(path string) error {
	log.Info().Str("file", path).Msg("开始从文件加载元数据")

	// 读取文件
	data, err := os.ReadFile(path)
	if err != nil {
		log.Error().Err(err).Str("file", path).Msg("读取文件失败")
		return fmt.Errorf("读取文件失败: %w", err)
	}

	// 解析JSON
	var cache MetadataCache
	if err = json.Unmarshal(data, &cache); err != nil {
		log.Error().Err(err).Str("file", path).Msg("解析JSON失败")
		return fmt.Errorf("解析JSON失败: %w", err)
	}

	// 初始化映射
	my.Nodes = make(map[string]*internal.Class)

	// 处理类定义
	for className, oldClass := range cache.Nodes {
		// 创建新的类实例
		class := &internal.Class{
			Name:        oldClass.Name,
			Table:       oldClass.Table,
			Description: oldClass.Description,
			Virtual:     oldClass.Virtual,
			PrimaryKeys: oldClass.PrimaryKeys,
			Fields:      make(map[string]*internal.Field),
			TableNames:  make(map[string]bool),
		}

		// 复制字段
		for fieldName, oldField := range oldClass.Fields {
			field := &internal.Field{
				Name:        oldField.Name,
				Column:      oldField.Column,
				Type:        oldField.Type,
				Description: oldField.Description,
				IsPrimary:   oldField.IsPrimary,
				Virtual:     oldField.Virtual,
			}

			// 如果有关系，复制关系信息
			if oldField.Relation != nil {
				field.Relation = &internal.Relation{
					SourceClass: oldField.Relation.SourceClass,
					SourceField: oldField.Relation.SourceField,
					TargetClass: oldField.Relation.TargetClass,
					TargetField: oldField.Relation.TargetField,
				}
			}

			class.Fields[fieldName] = field
		}

		// 复制表名映射
		for tableName, isOriginal := range oldClass.TableNames {
			class.TableNames[tableName] = isOriginal
		}

		// 添加到Nodes集合
		my.Nodes[className] = class

		// 添加表名索引，指向同一个实例
		my.Nodes[class.Table] = class

		// 添加其他表名索引
		for tableName := range class.TableNames {
			if tableName != class.Table {
				my.Nodes[tableName] = class
			}
		}
	}

	log.Info().
		Int("classes", len(cache.Nodes)).
		Msg("从文件加载元数据完成")

	return nil
}

// saveMetadataToFile 保存元数据到文件
func (my *Metadata) saveMetadataToFile(filePath string) error {
	log.Info().Str("file", filePath).Msg("开始保存元数据到文件")

	// 创建要保存的数据结构
	cache := MetadataCache{
		Nodes: my.Nodes,
	}

	// 序列化为JSON
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("序列化元数据失败")
		return fmt.Errorf("序列化元数据失败: %w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("创建目录失败")
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("写入元数据文件失败")
		return fmt.Errorf("写入元数据文件失败: %w", err)
	}

	log.Info().
		Int("classes", len(my.Nodes)).
		Msg("保存元数据到文件完成")

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
	if my.cfg.EnableCamelCase {
		return strcase.ToLowerCamel(rawName)
	}

	return rawName
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
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		return &internal.Class{
			Name:        node.Name,
			Table:       node.Table,
			Virtual:     node.Virtual,
			Fields:      node.Fields,
			PrimaryKeys: node.PrimaryKeys,
			Description: node.Description,
		}, true
	}
	return nil, false
}

// FindField 根据类名和字段名查找字段
func (my *Metadata) FindField(className, fieldName string, virtual bool) (*internal.Field, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		if field := node.GetField(fieldName); field != nil && field.Virtual == virtual {
			return field, true
		}
	}
	return nil, false
}

// GetForeignKey 获取外键关系(支持字段名或列名)
func (my *Metadata) GetForeignKey(sourceTable, nameOrColumn string) (*internal.Relation, bool) {
	if node, ok := my.Nodes[sourceTable]; ok {
		if field := node.GetField(nameOrColumn); field != nil {
			return field.Relation, field.Relation != nil
		}
	}
	return nil, false
}

// TableName 获取类的表名
func (my *Metadata) TableName(className string, virtual bool) (string, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		return node.Table, len(node.Table) > 0
	}
	return "", false
}

// ColumnName 获取字段的列名
func (my *Metadata) ColumnName(className, fieldName string, virtual bool) (string, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		if field := node.GetField(fieldName); field != nil && field.Virtual == virtual {
			return field.Column, len(field.Column) > 0
		}
	}
	return "", false
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

	// 获取配置中的表定义
	var tables []map[string]interface{}
	if err := my.v.UnmarshalKey("metadata.tables", &tables); err != nil {
		log.Error().Err(err).Msg("解析表配置失败")
		return fmt.Errorf("解析表配置失败: %w", err)
	}

	// 处理每个表
	for _, table := range tables {
		// 获取表名
		tableName, ok := table["name"].(string)
		if !ok || tableName == "" {
			log.Warn().Interface("table", table).Msg("表名无效")
			continue
		}

		// 检查是否应包含此表
		if !my.shouldIncludeTable(tableName) {
			log.Debug().Str("table", tableName).Msg("排除表")
			continue
		}

		// 获取表的显示名称（类名）
		className := tableName
		if displayName, ok := table["display_name"].(string); ok && displayName != "" {
			className = displayName
		} else {
			className = my.convertTableName(tableName)
		}

		// 获取表描述
		description := ""
		if desc, ok := table["description"].(string); ok {
			description = desc
		}

		// 创建类定义
		class := &internal.Class{
			Name:        className,
			Table:       tableName,
			Description: description,
			Fields:      make(map[string]*internal.Field),
			TableNames:  make(map[string]bool),
		}

		// 设置原始表名
		class.TableNames[tableName] = true

		// 处理主键
		if primaryKeys, ok := table["primary_keys"].([]interface{}); ok {
			for _, pk := range primaryKeys {
				if pkStr, ok := pk.(string); ok {
					class.PrimaryKeys = append(class.PrimaryKeys, pkStr)
				}
			}
		}

		// 处理字段
		if columns, ok := table["columns"].([]map[string]interface{}); ok {
			for _, colMap := range columns {
				// 获取字段名
				columnName, ok := colMap["name"].(string)
				if !ok || columnName == "" {
					continue
				}

				// 检查是否应包含此字段
				if !my.shouldIncludeField(columnName) {
					continue
				}

				// 获取字段的显示名称
				fieldName := columnName
				if displayName, ok := colMap["display_name"].(string); ok && displayName != "" {
					fieldName = displayName
				} else if my.cfg.EnableCamelCase {
					fieldName = strcase.ToLowerCamel(columnName)
				}

				// 获取字段类型
				fieldType := "string"
				if t, ok := colMap["type"].(string); ok {
					fieldType = t
				}

				// 获取字段描述
				fieldDesc := ""
				if desc, ok := colMap["description"].(string); ok {
					fieldDesc = desc
				}

				// 获取是否主键
				isPrimary := false
				if p, ok := colMap["is_primary"].(bool); ok {
					isPrimary = p
				}

				// 创建字段定义
				field := &internal.Field{
					Name:        fieldName,
					Column:      columnName,
					Type:        fieldType,
					Description: fieldDesc,
					IsPrimary:   isPrimary,
				}

				// 添加字段
				class.Fields[fieldName] = field

				// 如果是主键，添加到主键列表
				if isPrimary {
					class.PrimaryKeys = append(class.PrimaryKeys, columnName)
				}
			}
		}

		// 添加类定义到映射
		my.Nodes[className] = class
		my.Nodes[tableName] = class
	}

	log.Info().
		Int("classes", len(my.Nodes)).
		Msg("元数据加载完成")

	return nil
}
