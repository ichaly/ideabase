package gql

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/duke-git/lancet/v2/convertor"
	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/log"
	"github.com/jinzhu/inflection"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

func init() {
	inflection.AddUncountable("children")
	strcase.ConfigureAcronym("ID", "Id")
}

// Metadata 表示GraphQL元数据
type Metadata struct {
	v   *viper.Viper
	db  *gorm.DB
	cfg *internal.Config

	// 统一索引: 支持类名、表名、原始表名查找
	Nodes   map[string]*internal.Class `json:"nodes"`
	Version string                     `json:"version"`
}

// NewMetadata 创建一个新的元数据处理器
func NewMetadata(v *viper.Viper, d *gorm.DB) (*Metadata, error) {
	//初始化配置
	cfg := &internal.Config{Schema: internal.SchemaConfig{TypeMapping: dataTypes}}
	v.SetDefault("schema.schema", "public")
	v.SetDefault("schema.default-limit", 10)
	v.SetDefault("schema.enable-singular", true)
	v.SetDefault("schema.enable-camel-case", true)
	v.SetDefault("schema.table-prefix", []string{})

	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}

	my := &Metadata{
		v:     v,
		db:    d,
		cfg:   cfg,
		Nodes: make(map[string]*internal.Class),
		//使用当前时间戳初始化版本
		Version: time.Now().Format("20060102150405"),
	}

	// 加载元数据
	if err := my.loadMetadata(); err != nil {
		return nil, err
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

	// 处理完配置加载后，处理所有关系
	log.Info().Msg("处理所有关系信息")
	my.processAllRelationships()

	log.Info().
		Int("classes", len(my.Nodes)).
		Msg("元数据加载完成")
	return nil
}

// loadFromConfig 从配置加载元数据
func (my *Metadata) loadFromConfig() error {
	// 获取配置中的元数据定义
	classes := my.cfg.Metadata.Classes
	if len(classes) == 0 {
		log.Info().Msg("配置中没有定义元数据")
		return nil
	}

	log.Info().Msg("开始从配置加载元数据")

	// 遍历配置中的类定义
	for className, classConfig := range classes {
		log.Info().Str("class", className).Msg("处理类配置")

		// 确定表名，用于查找基类
		tableName := classConfig.Table
		baseClass, isFound := my.Nodes[tableName]

		var newClass *internal.Class

		// 确定类的状态如果基类不存在(虚拟类)或者基类名称与类名不一致(类别名)，则创建新类
		if !isFound || baseClass.Name != className {
			// 创建新类的情况
			log.Info().
				Str("class", className).
				Str("table", tableName).
				Bool("virtual", !isFound).
				Msg("创建新类")

			// 创建新类对象
			newClass = &internal.Class{
				Name:    className,
				Table:   tableName,
				Virtual: !isFound,
				Fields:  make(map[string]*internal.Field),
			}

			// 如果是类别名而非虚拟类，复制基类字段
			if isFound {
				if len(classConfig.Fields) > 0 {
					// 对原字段有改动则需要深度复制字段,避免有副作用
					my.copyClassFields(newClass, baseClass)
				} else {
					// 尽可能复用原类字段对象
					for fieldName, field := range baseClass.Fields {
						newClass.Fields[fieldName] = field
					}
				}
			}

			// 添加类到元数据
			my.Nodes[className] = newClass
			// 如果是类别名并且有表名，添加表名索引
			if isFound && tableName != className {
				my.Nodes[tableName] = newClass
			}
		} else {
			// 更新现有类的情况
			log.Info().
				Str("class", className).
				Msg("更新现有类")
			newClass = baseClass
		}

		// 统一处理字段过滤和字段配置，无论是新类还是现有类
		// 1. 更新类属性
		if classConfig.Description != "" {
			newClass.Description = classConfig.Description
		}
		if classConfig.Resolver != "" {
			newClass.Resolver = classConfig.Resolver
		}
		if len(classConfig.PrimaryKeys) > 0 {
			newClass.PrimaryKeys = classConfig.PrimaryKeys
		}
		// 2. 先应用字段过滤
		my.applyFieldFiltering(newClass, classConfig)
		// 3. 再处理配置的字段（覆盖或添加）
		my.processClassFields(newClass, classConfig.Fields)
	}

	// 处理关系
	my.processRelationships()

	log.Info().Int("classes", len(my.Nodes)).Msg("配置元数据加载完成")
	return nil
}

// 复制类字段 - 从基类到目标类
func (my *Metadata) copyClassFields(targetClass, sourceClass *internal.Class) {
	for fieldName, field := range sourceClass.Fields {
		// 只复制原始字段，非索引字段
		if field.Name == fieldName {
			// 深度复制字段
			newField := convertor.DeepClone(field)

			// 更新关系中的源类引用
			if newField.Relation != nil {
				newField.Relation.SourceClass = targetClass.Name
			}

			targetClass.AddField(newField)
		}
	}
}

// 应用字段过滤
func (my *Metadata) applyFieldFiltering(class *internal.Class, config *internal.ClassConfig) {
	// 如果指定了包含字段，则将所有不在包含列表中的字段移除
	if len(config.IncludeFields) > 0 {
		includeSet := make(map[string]bool)
		for _, fieldName := range config.IncludeFields {
			includeSet[fieldName] = true
		}

		// 找出并移除所有不在包含列表中的字段
		for fieldName, field := range class.Fields {
			if field.Name == fieldName && !includeSet[fieldName] {
				class.RemoveField(field)
			}
		}
	}

	// 移除配置中指定要排除的字段
	for _, fieldName := range config.ExcludeFields {
		if field := class.Fields[fieldName]; field != nil {
			class.RemoveField(field)
		}
	}
}

// 处理类字段
func (my *Metadata) processClassFields(class *internal.Class, fieldConfigs map[string]*internal.FieldConfig) {
	if fieldConfigs == nil {
		return
	}

	for fieldName, fieldConfig := range fieldConfigs {
		// 检查是否已存在此字段
		existingField := class.Fields[fieldName]

		if existingField != nil {
			// 更新现有字段
			my.updateField(existingField, fieldConfig)
		} else {
			// 添加新字段
			field := my.createField(class.Name, fieldName, fieldConfig)
			class.AddField(field)
		}
	}
}

// 更新字段
func (my *Metadata) updateField(field *internal.Field, config *internal.FieldConfig) {
	// 更新非空值
	if config.Description != "" {
		field.Description = config.Description
	}

	if config.Type != "" {
		field.Type = config.Type
	}

	if config.Column != "" {
		field.Column = config.Column
	}

	// 更新Resolver
	if config.Resolver != "" {
		field.Resolver = config.Resolver
	}

	// 覆盖布尔属性
	field.IsPrimary = config.IsPrimary
	field.IsUnique = config.IsUnique
	field.Nullable = config.IsNullable

	// 处理关系配置
	if config.Relation != nil {
		// 如果字段没有关系，则创建一个
		if field.Relation == nil {
			field.Relation = &internal.Relation{
				SourceField: field.Name,
			}
		}

		rel := field.Relation
		relConfig := config.Relation

		// 更新关系属性
		if relConfig.TargetClass != "" {
			rel.TargetClass = relConfig.TargetClass
		}

		if relConfig.TargetField != "" {
			rel.TargetField = relConfig.TargetField
		}

		if relConfig.Type != "" {
			rel.Type = internal.RelationType(relConfig.Type)
		}

		if relConfig.ReverseName != "" {
			rel.ReverseName = relConfig.ReverseName
		}

		// 处理Through配置
		if relConfig.Through != nil {
			if rel.Through == nil {
				rel.Through = &internal.Through{}
			}

			through := rel.Through
			throughConfig := relConfig.Through

			if throughConfig.Table != "" {
				through.Table = throughConfig.Table
			}

			if throughConfig.SourceKey != "" {
				through.SourceKey = throughConfig.SourceKey
			}

			if throughConfig.TargetKey != "" {
				through.TargetKey = throughConfig.TargetKey
			}

			if throughConfig.ClassName != "" {
				through.Name = throughConfig.ClassName
			}

			// 处理中间表字段
			if len(throughConfig.Fields) > 0 {
				if through.Fields == nil {
					through.Fields = make(map[string]*internal.Field)
				}

				for thrFieldName, thrFieldConfig := range throughConfig.Fields {
					// 检查是否存在
					existingThrField := through.Fields[thrFieldName]

					if existingThrField != nil {
						// 更新现有字段
						my.updateField(existingThrField, thrFieldConfig)
					} else {
						// 创建新字段
						thrField := my.createField(throughConfig.ClassName, thrFieldName, thrFieldConfig)
						through.Fields[thrFieldName] = thrField
					}
				}
			}
		}
	}
}

// 创建新字段
func (my *Metadata) createField(className, fieldName string, config *internal.FieldConfig) *internal.Field {
	// 如果没有指定列名，使用字段名
	column := config.Column
	if column == "" {
		column = fieldName
	}

	field := &internal.Field{
		Name:        fieldName,
		Column:      column,
		Type:        config.Type,
		Description: config.Description,
		Nullable:    config.IsNullable,
		IsPrimary:   config.IsPrimary,
		IsUnique:    config.IsUnique,
		Resolver:    config.Resolver,
	}

	// 处理关系配置
	if config.Relation != nil {
		field.Relation = &internal.Relation{
			SourceClass: className,
			SourceField: fieldName,
			TargetClass: config.Relation.TargetClass,
			TargetField: config.Relation.TargetField,
			Type:        internal.RelationType(config.Relation.Type),
			ReverseName: config.Relation.ReverseName,
		}

		// 处理Through配置
		if config.Relation.Through != nil {
			field.Relation.Through = &internal.Through{
				Table:     config.Relation.Through.Table,
				SourceKey: config.Relation.Through.SourceKey,
				TargetKey: config.Relation.Through.TargetKey,
				Name:      config.Relation.Through.ClassName,
			}

			// 处理中间表字段
			if len(config.Relation.Through.Fields) > 0 {
				field.Relation.Through.Fields = make(map[string]*internal.Field)

				for thrFieldName, thrFieldConfig := range config.Relation.Through.Fields {
					thrField := my.createField(config.Relation.Through.ClassName, thrFieldName, thrFieldConfig)
					field.Relation.Through.Fields[thrFieldName] = thrField
				}
			}
		}
	}

	return field
}

// 处理关系
func (my *Metadata) processRelationships() {
	// 建立所有类之间的关系
	for _, class := range my.Nodes {
		for _, field := range class.Fields {
			if field.Relation == nil {
				continue
			}

			// 确保关系的源类和字段已设置
			if field.Relation.SourceClass == "" {
				field.Relation.SourceClass = class.Name
			}

			if field.Relation.SourceField == "" {
				field.Relation.SourceField = field.Name
			}

			// 查找目标类
			targetClass := my.Nodes[field.Relation.TargetClass]
			if targetClass == nil {
				log.Warn().
					Str("class", class.Name).
					Str("field", field.Name).
					Str("targetClass", field.Relation.TargetClass).
					Msg("关系目标类不存在")
				continue
			}

			// 找到目标字段
			targetField := targetClass.Fields[field.Relation.TargetField]
			if targetField == nil {
				log.Warn().
					Str("class", class.Name).
					Str("field", field.Name).
					Str("targetClass", field.Relation.TargetClass).
					Str("targetField", field.Relation.TargetField).
					Msg("关系目标字段不存在")
				continue
			}

			// 如果目标字段没有反向关系，创建一个
			if targetField.Relation == nil {
				reverseName := field.Relation.ReverseName
				if reverseName == "" {
					// 如果没有指定反向名称，使用默认命名
					reverseName = my.generateReverseName(class.Name, field.Relation.Type)
				}

				reverseType := field.Relation.Type.Reverse()

				// 创建反向关系
				targetField.Relation = &internal.Relation{
					SourceClass: targetClass.Name,
					SourceField: targetField.Name,
					TargetClass: class.Name,
					TargetField: field.Name,
					Type:        reverseType,
					Reverse:     field.Relation,
				}
			}

			// 链接反向关系
			field.Relation.Reverse = targetField.Relation
		}
	}
}

// 生成反向关系名称
func (my *Metadata) generateReverseName(className string, relationType internal.RelationType) string {
	// 根据类名和关系类型生成合适的反向名称
	switch relationType {
	case internal.ONE_TO_MANY:
		return strcase.ToLowerCamel(inflection.Plural(className))
	case internal.MANY_TO_ONE:
		return strcase.ToLowerCamel(className)
	case internal.MANY_TO_MANY:
		return strcase.ToLowerCamel(inflection.Plural(className))
	default:
		return strcase.ToLowerCamel(className)
	}
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
		if field := node.Fields[fieldName]; field != nil && field.Virtual == virtual {
			return field, true
		}
	}
	return nil, false
}

// FindRelation 获取外键关系(支持字段名或列名)
func (my *Metadata) FindRelation(sourceTable, nameOrColumn string) (*internal.Relation, bool) {
	if node, ok := my.Nodes[sourceTable]; ok {
		if field := node.Fields[nameOrColumn]; field != nil {
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
		if field := node.Fields[fieldName]; field != nil && field.Virtual == virtual {
			return field.Column, len(field.Column) > 0
		}
	}
	return "", false
}

// processAllRelationships 处理所有关系
func (my *Metadata) processAllRelationships() {
	// 第一步：基本关系处理（确保原始关系信息正确）
	my.processRelationships()

	// 第二步：创建关系字段（添加实际可用于GraphQL的字段）
	my.createRelationshipFields()
}

// createRelationshipFields 根据关系创建关系字段
func (my *Metadata) createRelationshipFields() {
	log.Debug().Msg("开始创建关系字段")

	// 只保留反向关系字段映射
	reverseFields := make(map[string]map[string]*internal.Field)

	// 初始化反向字段映射 - 只在真正的类名上创建
	for className, class := range my.Nodes {
		if className == class.Name {
			reverseFields[className] = make(map[string]*internal.Field)
		}
	}

	// 处理所有类和字段的关系
	for className, class := range my.Nodes {
		// 跳过表名索引，只处理类名索引
		if className != class.Name {
			continue
		}

		for fieldName, field := range class.Fields {
			// 跳过非主字段或没有关系的字段
			if fieldName != field.Name || field.Relation == nil {
				continue
			}

			// 获取目标类
			relation := field.Relation
			targetClassName := relation.TargetClass
			targetClass := my.Nodes[targetClassName]

			// 验证目标类存在
			if targetClass == nil {
				log.Warn().Str("class", className).Str("field", fieldName).
					Str("targetClass", targetClassName).Msg("关系目标类不存在")
				continue
			}

			// 根据关系类型创建关系字段
			switch relation.Type {
			case internal.MANY_TO_MANY:
				// 创建多对多关系字段
				relName := my.uniqueFieldName(class,
					strcase.ToLowerCamel(inflection.Plural(targetClassName)))

				class.Fields[relName] = &internal.Field{
					Type:           targetClassName,
					Name:           relName,
					Virtual:        true,
					IsCollection:   true,
					Nullable:       false,
					Description:    "多对多关联的" + targetClassName + "列表",
					SourceRelation: relation,
				}

				// 处理中间表
				if relation.Through != nil {
					// 直接从 Nodes 中查找表对应的类
					throughTable := relation.Through.Table
					throughClass := my.Nodes[throughTable]

					if throughClass != nil {
						// 创建中间表关系字段
						throughFieldName := my.uniqueFieldName(class,
							strcase.ToLowerCamel(inflection.Plural(throughClass.Name)))

						class.Fields[throughFieldName] = &internal.Field{
							Type:         throughClass.Name,
							Name:         throughFieldName,
							Virtual:      true,
							IsCollection: true,
							Nullable:     false,
							Description:  "关联的" + throughClass.Name + "记录列表",
							IsThrough:    true, // 标记为中间表字段，方便渲染时筛选
						}
					}
				}

			case internal.ONE_TO_MANY:
				// 创建一对多关系字段
				relName := my.uniqueFieldName(class,
					strcase.ToLowerCamel(inflection.Plural(targetClassName)))

				class.Fields[relName] = &internal.Field{
					Type:           targetClassName,
					Name:           relName,
					Virtual:        true,
					IsCollection:   true,
					Nullable:       false,
					Description:    "关联的" + targetClassName + "列表",
					SourceRelation: relation,
				}

			case internal.MANY_TO_ONE:
				// 创建多对一关系字段
				relName := my.uniqueFieldName(class,
					strcase.ToLowerCamel(targetClassName))

				class.Fields[relName] = &internal.Field{
					Type:           targetClassName,
					Name:           relName,
					Virtual:        true,
					IsCollection:   false,
					Nullable:       field.Nullable,
					Description:    "关联的" + targetClassName,
					SourceRelation: relation,
				}

				// 检查是否需要创建反向关系字段
				reverseExists := false
				for _, tf := range targetClass.Fields {
					if tf.IsCollection && tf.Type == className && tf.Virtual {
						reverseExists = true
						break
					}
				}

				if !reverseExists {
					// 创建反向关系字段
					reverseName := my.uniqueFieldName(targetClass,
						strcase.ToLowerCamel(inflection.Plural(className)))

					reverseFields[targetClassName][reverseName] = &internal.Field{
						Type:           className,
						Name:           reverseName,
						Virtual:        true,
						IsCollection:   true,
						Nullable:       false,
						Description:    "关联的" + className + "列表",
						SourceRelation: relation.Reverse,
					}
				}

			case internal.RECURSIVE:
				// 处理递归关系
				if strings.HasSuffix(fieldName, "Id") || strings.HasSuffix(fieldName, "ID") {
					// 创建父级关系字段
					parentName := my.uniqueFieldName(class, "parent")
					class.Fields[parentName] = &internal.Field{
						Type:           className,
						Name:           parentName,
						Virtual:        true,
						IsCollection:   false,
						Nullable:       true,
						Description:    "父" + className + "对象",
						SourceRelation: relation,
					}

					// 创建子级关系字段
					childrenName := my.uniqueFieldName(targetClass, "children")
					targetClass.Fields[childrenName] = &internal.Field{
						Type:           className,
						Name:           childrenName,
						Virtual:        true,
						IsCollection:   true,
						Nullable:       false,
						Description:    "子" + className + "列表",
						SourceRelation: relation.Reverse,
					}
				}
			}
		}
	}

	// 添加收集的反向关系字段
	for className, fields := range reverseFields {
		if class := my.Nodes[className]; class != nil {
			for fieldName, field := range fields {
				class.Fields[fieldName] = field
			}
		}
	}

	log.Debug().Msg("关系字段创建完成")
}

// uniqueFieldName 确保字段名在类中唯一
func (my *Metadata) uniqueFieldName(class *internal.Class, baseName string) string {
	fieldName := baseName
	counter := 1

	// 直接检查字段是否存在
	for class.Fields[fieldName] != nil {
		fieldName = baseName + strconv.Itoa(counter)
		counter++
	}

	return fieldName
}
