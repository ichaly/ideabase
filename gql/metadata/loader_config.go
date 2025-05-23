package metadata

import (
	"fmt"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/jinzhu/inflection"
	"github.com/mohae/deepcopy"
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
func (my *ConfigLoader) Support() bool {
	return my.cfg != nil && len(my.cfg.Metadata.Classes) > 0
}

// Load 从配置加载元数据
func (my *ConfigLoader) Load(h Hoster) error {
	// 第一次遍历：加载主类
	for indexName, classConfig := range my.cfg.Metadata.Classes {
		className := ConvertClassName(classConfig.Table, my.cfg.Metadata)
		if indexName == classConfig.Table || indexName == className {
			newClass := my.buildClassFromConfig(indexName, classConfig, nil)
			newClass.Name = newClass.Table
			h.PutClass(indexName, newClass)
			h.PutClass(newClass.Table, newClass)
		}
	}
	// 第二次遍历：加载别名类
	for indexName, classConfig := range my.cfg.Metadata.Classes {
		isVirtual := classConfig.Table == ""
		className := ConvertClassName(classConfig.Table, my.cfg.Metadata)
		if !(indexName == classConfig.Table || indexName == className) {
			var baseClass *internal.Class
			if !isVirtual {
				if classConfig.Override {
					if c, ok := h.GetClass(classConfig.Table); ok {
						baseClass = c
					} else if c, ok := h.GetClass(className); ok {
						baseClass = c
					}
					if baseClass == nil {
						return fmt.Errorf("配置别名类 %s 覆盖主类 %s 失败: 主类不存在", indexName, classConfig.Table)
					}
				} else {
					if c, ok := h.GetClass(classConfig.Table); ok {
						baseClass = c
					} else if c, ok := h.GetClass(className); ok {
						baseClass = c
					}
				}
			}
			newClass := my.buildClassFromConfig(indexName, classConfig, baseClass)
			h.PutClass(indexName, newClass)
		}
	}
	return nil
}

// buildClassFromConfig 根据ClassConfig和可选baseClass构建Class对象
func (my *ConfigLoader) buildClassFromConfig(className string, classConfig *internal.ClassConfig, baseClass *internal.Class) *internal.Class {
	isVirtual := classConfig.Table == ""
	var newClass *internal.Class
	if baseClass != nil {
		// 复制或覆盖
		newClass = deepcopy.Copy(baseClass).(*internal.Class)
		newClass.Name = className
	} else {
		newClass = &internal.Class{
			Name:    className,
			Table:   classConfig.Table,
			Virtual: isVirtual,
			Fields:  make(map[string]*internal.Field),
		}
	}
	if classConfig.Description != "" {
		newClass.Description = classConfig.Description
	}
	if classConfig.Resolver != "" {
		newClass.Resolver = classConfig.Resolver
	}
	if len(classConfig.PrimaryKeys) > 0 {
		newClass.PrimaryKeys = classConfig.PrimaryKeys
	}
	my.applyFieldFilter(newClass, classConfig)
	my.applyFieldConfig(newClass, classConfig.Fields)
	return newClass
}

// 应用字段过滤
func (my *ConfigLoader) applyFieldFilter(class *internal.Class, config *internal.ClassConfig) {
	if len(config.IncludeFields) > 0 {
		includeSet := make(map[string]bool)
		for _, fieldName := range config.IncludeFields {
			includeSet[fieldName] = true
		}
		for fieldName := range class.Fields {
			if !includeSet[fieldName] {
				delete(class.Fields, fieldName)
			}
		}
	}
	for _, fieldName := range config.ExcludeFields {
		delete(class.Fields, fieldName)
	}
}

// 处理类字段
func (my *ConfigLoader) applyFieldConfig(class *internal.Class, fieldConfigs map[string]*internal.FieldConfig) error {
	if fieldConfigs == nil {
		return nil
	}
	config := my.cfg.Metadata

	// 1. 分组排序
	var tableFields, classFields, overrideFields, aliasFields, virtualFields []string
	for fieldName, fieldConfig := range fieldConfigs {
		switch {
		case fieldConfig.Column == "":
			virtualFields = append(virtualFields, fieldName)
		case fieldConfig.Override:
			overrideFields = append(overrideFields, fieldName)
		case fieldName == fieldConfig.Column:
			tableFields = append(tableFields, fieldName)
		case fieldName == ConvertFieldName(fieldConfig.Column, config):
			classFields = append(classFields, fieldName)
		default:
			aliasFields = append(aliasFields, fieldName)
		}
	}
	orderedFields := append(tableFields, classFields...)
	orderedFields = append(orderedFields, overrideFields...)
	orderedFields = append(orderedFields, aliasFields...)
	orderedFields = append(orderedFields, virtualFields...)

	fields := make(map[string]*internal.Field)
	for _, fieldName := range orderedFields {
		fieldConfig := fieldConfigs[fieldName]
		canonName := ConvertFieldName(fieldConfig.Column, config)

		// 虚拟字段
		if fieldConfig.Column == "" {
			fields[fieldName] = createField(class.Name, fieldName, fieldConfig)
			continue
		}

		// 列字段、标准字段、覆盖字段统一处理
		if fieldName == fieldConfig.Column || fieldName == canonName || fieldConfig.Override {
			baseField, ok := class.Fields[fieldConfig.Column]
			if ok {
				baseField.Name = fieldName
				updateField(baseField, fieldConfig)
				fields[fieldConfig.Column] = baseField
			} else {
				field := createField(class.Name, fieldName, fieldConfig)
				fields[fieldConfig.Column] = field
			}
			continue
		}

		// 别名字段（必须依赖基础字段）
		baseField, ok := fields[fieldConfig.Column]
		if !ok {
			return fmt.Errorf("别名字段 %s 必须有基础字段 %s", fieldName, fieldConfig.Column)
		}
		field := deepcopy.Copy(baseField).(*internal.Field)
		field.Name = fieldName
		updateField(field, fieldConfig)
		fields[fieldName] = field
	}
	class.Fields = fields
	return nil
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
	field := &internal.Field{
		Name:        fieldName,
		Column:      config.Column,
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

// ConvertClassName 根据配置将表名转换为类名（去前缀、单数化、驼峰化等）
func ConvertClassName(tableName string, config internal.MetadataConfig) string {
	className := tableName
	// 去前缀
	for _, prefix := range config.TablePrefix {
		if strings.HasPrefix(className, prefix) {
			className = strings.TrimPrefix(className, prefix)
			break
		}
	}
	// 单数化
	if config.UseSingular {
		className = inflection.Singular(className)
	}
	// 驼峰
	if config.UseCamel {
		className = strcase.ToCamel(className)
	}

	return className
}

// ConvertFieldName 根据配置将字段名转换为小驼峰
func ConvertFieldName(columnName string, config internal.MetadataConfig) string {
	fieldName := columnName
	if config.UseCamel {
		fieldName = strcase.ToLowerCamel(fieldName)
	}
	return fieldName
}
