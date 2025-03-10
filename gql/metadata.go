package gql

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/log"
	"github.com/jinzhu/inflection"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

//go:embed assets/tpl/*
var templates embed.FS

func init() {
	inflection.AddUncountable("children")
	strcase.ConfigureAcronym("ID", "Id")
}

// Metadata 表示GraphQL元数据
type Metadata struct {
	v   *viper.Viper
	db  *gorm.DB
	cfg *internal.Config
	tpl *template.Template

	// 统一索引: 支持类名、表名、原始表名查找
	Nodes   map[string]*internal.Class `json:"nodes"`
	Version string                     `json:"version"`
}

// NewMetadata 创建一个新的元数据处理器
func NewMetadata(v *viper.Viper, d *gorm.DB) (*Metadata, error) {
	//初始化模板
	tpl, err := template.ParseFS(templates, "assets/tpl/*.tpl")
	if err != nil {
		return nil, err
	}

	//初始化配置
	cfg := &internal.Config{Schema: internal.SchemaConfig{TypeMapping: dataTypes}}
	v.SetDefault("schema.default-limit", 10)
	v.SetDefault("schema.enable-camel-case", true)
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
		//使用当前时间戳初始化版本
		Version: time.Now().Format("20060102150405"),
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

	mode := my.cfg.Mode
	name := fmt.Sprintf("metadata.%s.json", mode)
	path := filepath.Join(my.cfg.Root, "cfg", name)

	if mode == "dev" {
		log.Info().Msg("从数据库加载元数据")
		if err := my.loadDatabase(); err != nil {
			log.Error().Err(err).Msg("从数据库加载元数据失败")
			return fmt.Errorf("加载数据库元数据失败: %w", err)
		}

		log.Info().Str("path", path).Msg("保存元数据到预设文件")
		if err := my.saveToFile(path); err != nil {
			log.Error().Err(err).Str("path", path).Msg("保存元数据到预设文件失败")
			return fmt.Errorf("保存元数据缓存失败: %w", err)
		}
	} else {
		log.Info().Msg("从预设文件加载元数据")
		if err := my.loadFromFile(path); err != nil {
			log.Error().Err(err).Msg("从预设文件加载元数据失败")
			return fmt.Errorf("从预设文件加载元数据失败: %w", err)
		}
	}

	log.Info().Msg("使用配置中的元数据定义并合并")
	if err := my.loadFromConfig(); err != nil {
		log.Error().Err(err).Msg("加载配置元数据失败")
		return fmt.Errorf("加载配置元数据失败: %w", err)
	}

	log.Info().
		Int("classes", len(my.Nodes)).
		Msg("元数据加载完成")
	return nil
}

// loadFromConfig 从配置加载元数据
func (my *Metadata) loadFromConfig() error {
	tables := my.cfg.Metadata.Tables
	if len(tables) == 0 {
		return nil
	}

	for tableName, table := range tables {
		if tableName == "" || !my.shouldIncludeTable(tableName) {
			continue
		}

		// 确定类名
		className := my.convertTableName(tableName)
		if table.Name != "" {
			className = table.Name
		}

		// 获取或创建class
		class, exists := my.Nodes[className]
		if !exists {
			class = &internal.Class{
				Name:   className,
				Table:  tableName,
				Fields: make(map[string]*internal.Field),
			}
			my.Nodes[className] = class
		}

		// 更新class属性
		if table.Description != "" {
			class.Description = table.Description
		}
		if len(table.PrimaryKeys) > 0 {
			class.PrimaryKeys = table.PrimaryKeys
		}
		class.Virtual = table.Virtual

		// 合并字段和处理关系
		for columnName, column := range table.Columns {
			if columnName == "" || !my.shouldIncludeField(columnName) {
				continue
			}

			// 确定字段名
			fieldName := my.convertFieldName(tableName, columnName)
			if column.Name != "" {
				fieldName = column.Name
			}

			// 获取或创建field
			field, exists := class.Fields[fieldName]
			if !exists {
				field = &internal.Field{
					Name:   fieldName,
					Column: columnName,
				}
				class.Fields[fieldName] = field
			}

			// 更新field属性
			if column.Type != "" {
				field.Type = column.Type
			}
			if column.Description != "" {
				field.Description = column.Description
			}
			if column.IsPrimary {
				field.IsPrimary = true
			}

			// 处理字段关系
			if column.Relation != nil {
				// 转换关系中的类名和字段名
				targetClassName := column.Relation.TargetClass
				targetFieldName := column.Relation.TargetField

				// 确保使用转换后的类名和字段名
				convertedTargetClassName := my.convertTableName(targetClassName)
				convertedTargetFieldName := my.convertFieldName(targetClassName, targetFieldName)

				// 设置关系类型
				kind := internal.RelationType("").FromString(column.Relation.Type)

				// 设置关系
				field.Relation = &internal.Relation{
					SourceClass: class.Name,
					SourceField: field.Name,
					TargetClass: convertedTargetClassName,
					TargetField: convertedTargetFieldName,
					Type:        kind,
				}

				// 如果是多对多关系，设置中间表配置
				if kind == internal.MANY_TO_MANY && column.Relation.Through != nil {
					field.Relation.Through = &internal.Through{
						Table:     column.Relation.Through.Table,
						SourceKey: column.Relation.Through.SourceKey,
						TargetKey: column.Relation.Through.TargetKey,
					}
				}

				// 获取目标类
				targetClass := my.Nodes[convertedTargetClassName]
				if targetClass != nil {
					// 创建反向关系(非递归关系)
					if kind != internal.RECURSIVE {
						reverseField := &internal.Field{
							Name:    convertedTargetFieldName,
							Virtual: true,
							Relation: &internal.Relation{
								SourceClass: convertedTargetClassName,
								SourceField: convertedTargetFieldName,
								TargetClass: class.Name,
								TargetField: field.Name,
								Type:        kind,
							},
						}

						// 如果是多对多关系，设置反向关系的中间表配置
						if kind == internal.MANY_TO_MANY && column.Relation.Through != nil {
							reverseField.Relation.Through = &internal.Through{
								Table:     column.Relation.Through.Table,
								SourceKey: column.Relation.Through.TargetKey,
								TargetKey: column.Relation.Through.SourceKey,
							}
						}

						targetClass.Fields[reverseField.Name] = reverseField

						// 建立双向引用
						field.Relation.Reverse = reverseField.Relation
						reverseField.Relation.Reverse = field.Relation
					}
				}
			}
		}

		// 更新表名索引
		if className != tableName {
			my.Nodes[tableName] = class
		}
	}

	return nil
}

// loadDatabase 从数据库加载元数据
func (my *Metadata) loadDatabase() error {
	log.Info().Msg("开始从数据库加载元数据")

	// 创建数据库加载器
	loader, err := metadata.NewDatabaseLoader(my.db, my.cfg.Schema.Schema)
	if err != nil {
		log.Error().Err(err).Msg("创建数据库加载器失败")
		return err
	}
	log.Debug().Str("schema", my.cfg.Schema.Schema).Msg("创建数据库加载器")

	// 加载元数据
	log.Debug().Msg("开始从数据库加载元数据")
	classes, err := loader.LoadMetadata()
	if err != nil {
		log.Error().Err(err).Msg("数据库加载元数据失败")
		return err
	}
	log.Debug().Int("tables", len(classes)).Msg("数据库元数据加载完成")

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

		// 更新类名和表名
		class.Name = className
		class.Table = tableName

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
				renamedFields++
			}
		}

		if filteredFields > 0 || renamedFields > 0 {
			log.Debug().
				Str("table", tableName).
				Int("filtered", filteredFields).
				Int("renamed", renamedFields).
				Msg("字段处理")
		}

		// 更新主键列表（转换字段名）
		for i, pkName := range class.PrimaryKeys {
			class.PrimaryKeys[i] = my.convertFieldName(tableName, pkName)
		}

		// 添加到Nodes集合(支持通过类名和表名查找)
		my.Nodes[className] = class
		if className != tableName {
			my.Nodes[tableName] = class
		}
	}

	// 处理关系的类名和字段名转换
	log.Debug().Msg("开始处理关系的名称转换")
	for _, class := range my.Nodes {
		for _, field := range class.Fields {
			if field.Relation != nil {
				// 转换原始表名和字段名为转换后的名称
				sourceClassName := my.convertTableName(field.Relation.SourceClass)
				sourceFieldName := my.convertFieldName(field.Relation.SourceClass, field.Relation.SourceField)
				targetClassName := my.convertTableName(field.Relation.TargetClass)
				targetFieldName := my.convertFieldName(field.Relation.TargetClass, field.Relation.TargetField)

				// 更新关系字段
				field.Relation.SourceClass = sourceClassName
				field.Relation.SourceField = sourceFieldName
				field.Relation.TargetClass = targetClassName
				field.Relation.TargetField = targetFieldName
			}
		}
	}

	return nil
}

// loadFromFile 从文件加载元数据
func (my *Metadata) loadFromFile(path string) error {
	log.Info().Str("file", path).Msg("开始从文件加载元数据")

	data, err := os.ReadFile(path)
	if err != nil {
		log.Error().Err(err).Str("file", path).Msg("读取文件失败")
		return fmt.Errorf("读取文件失败: %w", err)
	}

	// 创建一个临时结构来存储文件内容
	var mate Metadata

	// 解析JSON数据
	if err = json.Unmarshal(data, &mate); err != nil {
		log.Error().Err(err).Str("file", path).Msg("解析JSON失败")
		return fmt.Errorf("解析JSON失败: %w", err)
	}

	// 更新元数据
	my.Version = mate.Version
	my.Nodes = make(map[string]*internal.Class)

	// 处理所有类
	for className, class := range mate.Nodes {
		// 只添加大写开头的类名（主类名）
		if className == class.Name {
			// 初始化字段映射
			fields := make(map[string]*internal.Field)

			// 处理每个字段
			for fieldName, field := range class.Fields {
				// 添加字段名索引
				fields[fieldName] = field

				// 如果列名与字段名不同，添加列名索引
				if field.Column != "" && field.Column != fieldName {
					fields[field.Column] = field
				}
			}

			// 更新类的字段映射
			class.Fields = fields

			// 添加类到Nodes映射
			my.Nodes[className] = class

			// 添加表名索引，指向同一个实例
			if class.Table != class.Name {
				my.Nodes[class.Table] = class
			}
		}
	}

	// 不再需要专门的反向引用重建循环，因为这些引用已经在JSON中被正确序列化和反序列化

	log.Info().Int("classes", len(my.Nodes)).Msg("从文件加载元数据完成")
	return nil
}

// saveMetadataToFile 保存元数据到文件
func (my *Metadata) saveToFile(filePath string) error {
	log.Info().Str("file", filePath).Msg("开始保存元数据到文件")

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("创建目录失败")
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 使用自定义序列化为JSON
	data, err := json.MarshalIndent(my, "", "  ")
	if err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("序列化元数据失败")
		return fmt.Errorf("序列化元数据失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("写入元数据文件失败")
		return fmt.Errorf("写入元数据文件失败: %w", err)
	}

	log.Info().Int("classes", len(my.Nodes)).Msg("保存元数据到文件完成")
	return nil
}

// convertTableName 转换表名
func (my *Metadata) convertTableName(rawName string) string {
	// 去除前缀
	name := rawName

	// 检查所有前缀
	for _, prefix := range my.cfg.Schema.TablePrefix {
		if prefix != "" && strings.HasPrefix(name, prefix) {
			name = name[len(prefix):]
			break // 一旦找到匹配的前缀就停止
		}
	}

	// 应用自定义映射
	if mappedName, ok := my.cfg.Schema.TableMapping[name]; ok {
		return mappedName
	}

	// 根据配置决定是否将表名转换为单数形式
	if my.cfg.Schema.EnableSingular {
		//为了支持users_roles类似的表名，按照下划线分割后在转换
		if strings.Contains(name, "_") {
			parts := strings.Split(name, "_")
			for i, part := range parts {
				parts[i] = inflection.Singular(part)
			}
			name = strings.Join(parts, "_")
		} else {
			name = inflection.Singular(name)
		}
	}

	// 转换为驼峰命名
	if my.cfg.Schema.EnableCamelCase {
		name = strcase.ToCamel(name)
	}

	return name
}

// convertFieldName 转换字段名
func (my *Metadata) convertFieldName(tableName, rawName string) string {
	// 应用自定义映射
	key := tableName + "." + rawName
	if mappedName, ok := my.cfg.Schema.FieldMapping[key]; ok {
		return mappedName
	}
	if mappedName, ok := my.cfg.Schema.FieldMapping[rawName]; ok {
		return mappedName
	}

	// 转换为驼峰命名
	if my.cfg.Schema.EnableCamelCase {
		return strcase.ToLowerCamel(rawName)
	}

	return rawName
}

// shouldIncludeTable 检查是否应该包含表
func (my *Metadata) shouldIncludeTable(tableName string) bool {
	// 检查排除列表
	for _, excluded := range my.cfg.Schema.ExcludeTables {
		if excluded == tableName {
			return false
		}
	}

	// 检查包含列表
	if len(my.cfg.Schema.IncludeTables) > 0 {
		for _, included := range my.cfg.Schema.IncludeTables {
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
	for _, excluded := range my.cfg.Schema.ExcludeFields {
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

// MarshalJSON 自定义JSON序列化
func (my *Metadata) MarshalJSON() ([]byte, error) {
	// 仅导出key和类名相同的节点
	nodes := make(map[string]*internal.Class)
	for key, class := range my.Nodes {
		if key == class.Name {
			// 直接使用原始对象，减少字段复制
			nodes[key] = class
		}
	}
	return json.Marshal(Metadata{
		Nodes:   nodes,
		Version: my.Version,
	})
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

// FindRelation 获取外键关系(支持字段名或列名)
func (my *Metadata) FindRelation(sourceTable, nameOrColumn string) (*internal.Relation, bool) {
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
