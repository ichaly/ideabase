package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"gorm.io/gorm"
)

// ConfigLoader 配置元数据加载器
// 实现Loader接口
type ConfigLoader struct {
	cfg *internal.Config
}

// NewConfigLoader 创建配置加载器
func NewConfigLoader(cfg *internal.Config) *ConfigLoader {
	return &ConfigLoader{cfg: cfg}
}

func (my *ConfigLoader) Name() string  { return LoaderConfig }
func (my *ConfigLoader) Priority() int { return 100 }

// Support 判断是否支持配置加载（通常总是支持）
func (my *ConfigLoader) Support(cfg *internal.Config, db *gorm.DB) bool {
	return true
}

// Load 从配置加载元数据
func (my *ConfigLoader) Load(h Hoster) error {
	// 获取配置中的元数据定义
	classes := my.cfg.Metadata.Classes
	if len(classes) == 0 {
		return nil
	}

	// 遍历配置中的类定义
	for className, classConfig := range classes {
		// 确定表名，用于查找基类
		tableName := classConfig.Table
		baseClass, isFound := h.GetNode(tableName)

		var newClass *internal.Class

		// 确定类的状态如果基类不存在(虚拟类)或者基类名称与类名不一致(类别名)，则创建新类
		if !isFound || baseClass.Name != className {
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
					copyClassFields(newClass, baseClass)
				} else {
					for fieldName, field := range baseClass.Fields {
						newClass.Fields[fieldName] = field
					}
				}
			}
		} else {
			// 更新现有类的情况
			newClass = baseClass
		}

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
		applyFieldFilter(newClass, classConfig)
		// 3. 再处理配置的字段（覆盖或添加）
		applyFieldConfig(newClass, classConfig.Fields)

		// 4. 最后添加到元数据
		h.PutNode(newClass)
	}

	return nil
}

// 复制类字段 - 从基类到目标类
func copyClassFields(targetClass, sourceClass *internal.Class) {
	for fieldName, field := range sourceClass.Fields {
		if field.Name == fieldName {
			newField := *field // 浅拷贝
			if newField.Relation != nil {
				newField.Relation.SourceClass = targetClass.Name
			}
			targetClass.Fields[fieldName] = &newField
		}
	}
}

// 应用字段过滤
func applyFieldFilter(class *internal.Class, config *internal.ClassConfig) {
	if len(config.IncludeFields) > 0 {
		includeSet := make(map[string]bool)
		for _, fieldName := range config.IncludeFields {
			includeSet[fieldName] = true
		}
		for fieldName, field := range class.Fields {
			if field.Name == fieldName && !includeSet[fieldName] {
				delete(class.Fields, fieldName)
			}
		}
	}
	for _, fieldName := range config.ExcludeFields {
		delete(class.Fields, fieldName)
	}
}

// 处理类字段
func applyFieldConfig(class *internal.Class, fieldConfigs map[string]*internal.FieldConfig) {
	if fieldConfigs == nil {
		return
	}
	for fieldName, fieldConfig := range fieldConfigs {
		existingField := class.Fields[fieldName]
		if existingField != nil {
			updateField(existingField, fieldConfig)
		} else {
			field := createField(class.Name, fieldName, fieldConfig)
			class.Fields[fieldName] = field
		}
	}
}

// 更新字段
func updateField(field *internal.Field, config *internal.FieldConfig) {
	if config.Description != "" {
		field.Description = config.Description
	}
	if config.Type != "" {
		field.Type = config.Type
	}
	if config.Column != "" {
		field.Column = config.Column
	}
	if config.Resolver != "" {
		field.Resolver = config.Resolver
	}
	field.IsPrimary = config.IsPrimary
	field.IsUnique = config.IsUnique
	field.Nullable = config.IsNullable
	if config.Relation != nil {
		if field.Relation == nil {
			field.Relation = &internal.Relation{
				SourceField: field.Name,
			}
		}
		rel := field.Relation
		relConfig := config.Relation
		if relConfig.TargetClass != "" {
			rel.TargetClass = relConfig.TargetClass
		}
		if relConfig.TargetField != "" {
			rel.TargetField = relConfig.TargetField
		}
		if relConfig.Type != "" {
			rel.Type = internal.RelationType(relConfig.Type)
		}
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
			if through.Name == "" && through.Table != "" {
				through.Name = through.Table // 简化处理
			}
			if len(throughConfig.Fields) > 0 {
				if through.Fields == nil {
					through.Fields = make(map[string]*internal.Field)
				}
				for thrFieldName, thrFieldConfig := range throughConfig.Fields {
					existingThrField := through.Fields[thrFieldName]
					if existingThrField != nil {
						updateField(existingThrField, thrFieldConfig)
					} else {
						thrField := createField(through.Name, thrFieldName, thrFieldConfig)
						through.Fields[thrFieldName] = thrField
					}
				}
			}
		}
	}
}

// 创建新字段
func createField(className, fieldName string, config *internal.FieldConfig) *internal.Field {
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
	if config.Relation != nil {
		field.Relation = &internal.Relation{
			SourceClass: className,
			SourceField: fieldName,
			TargetClass: config.Relation.TargetClass,
			TargetField: config.Relation.TargetField,
			Type:        internal.RelationType(config.Relation.Type),
		}
		switch field.Relation.Type {
		case internal.MANY_TO_MANY, internal.ONE_TO_MANY:
			field.IsList = true
		case internal.MANY_TO_ONE, internal.RECURSIVE:
			field.IsList = false
		}
		if config.Relation.Through != nil {
			field.Relation.Through = &internal.Through{
				Table:     config.Relation.Through.Table,
				SourceKey: config.Relation.Through.SourceKey,
				TargetKey: config.Relation.Through.TargetKey,
				Name:      config.Relation.Through.ClassName,
			}
			if field.Relation.Through.Name == "" && field.Relation.Through.Table != "" {
				field.Relation.Through.Name = field.Relation.Through.Table
			}
			if len(config.Relation.Through.Fields) > 0 {
				field.Relation.Through.Fields = make(map[string]*internal.Field)
				for thrFieldName, thrFieldConfig := range config.Relation.Through.Fields {
					thrField := createField(field.Relation.Through.Name, thrFieldName, thrFieldConfig)
					field.Relation.Through.Fields[thrFieldName] = thrField
				}
			}
		}
	}
	return field
}
