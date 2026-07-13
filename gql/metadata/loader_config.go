package metadata

import (
	"fmt"
	"github.com/ichaly/ideabase/gql/internal"
	"strings"

	"github.com/huandu/go-clone"
	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/utl"
	"github.com/jinzhu/inflection"
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
func (my *ConfigLoader) Load(h protocol.Hoster) error {
	// 1. 分组排序
	var tableClasses, canonClasses, overrideClasses, aliasClasses, virtualClasses []string
	for className, classConfig := range my.cfg.Metadata.Classes {
		switch {
		case classConfig.Table == "":
			virtualClasses = append(virtualClasses, className)
		case classConfig.Override:
			overrideClasses = append(overrideClasses, className)
		case className == classConfig.Table:
			tableClasses = append(tableClasses, className)
		case className == ConvertClassName(classConfig.Table, my.cfg.Metadata):
			canonClasses = append(canonClasses, className)
		default:
			aliasClasses = append(aliasClasses, className)
		}
	}
	orderedClasses := append(tableClasses, canonClasses...)
	orderedClasses = append(orderedClasses, overrideClasses...)
	orderedClasses = append(orderedClasses, aliasClasses...)
	orderedClasses = append(orderedClasses, virtualClasses...)

	for _, className := range orderedClasses {
		classConfig := my.cfg.Metadata.Classes[className]
		canonName := ConvertClassName(classConfig.Table, my.cfg.Metadata)

		// 虚拟类
		if classConfig.Table == "" {
			class, err := my.buildClassFromConfig(className, classConfig, nil)
			if err != nil {
				return err
			}
			h.PutNode(className, class)
			continue
		}

		// 主类/标准类/覆盖类统一处理
		if className == classConfig.Table || className == canonName || classConfig.Override {
			baseClass, _ := h.GetNode(classConfig.Table) // 未命中时为nil，按新建处理
			class, err := my.buildClassFromConfig(className, classConfig, baseClass)
			if err != nil {
				return err
			}
			h.PutNode(classConfig.Table, class)
			continue
		}

		// 别名类（必须依赖基础类）
		baseClass, ok := h.GetNode(classConfig.Table)
		if !ok {
			return fmt.Errorf("别名类 %s 必须有基础类 %s", className, classConfig.Table)
		}
		class, err := my.buildClassFromConfig(className, classConfig, baseClass)
		if err != nil {
			return err
		}
		h.PutNode(className, class)
	}
	return nil
}

// buildClassFromConfig 根据ClassConfig和可选baseClass构建Class对象
func (my *ConfigLoader) buildClassFromConfig(className string, classConfig *internal.ClassConfig, baseClass *protocol.Class) (*protocol.Class, error) {
	isVirtual := classConfig.Table == ""
	var newClass *protocol.Class
	if baseClass != nil {
		// 复制或覆盖
		newClass = clone.Slowly(baseClass).(*protocol.Class)
		newClass.Name = className
	} else {
		newClass = &protocol.Class{
			Name:    className,
			Table:   classConfig.Table,
			Virtual: isVirtual,
			Fields:  make(map[string]*protocol.Field),
		}
	}
	if classConfig.Description != "" {
		newClass.Description = classConfig.Description
	}
	if len(classConfig.PrimaryKeys) > 0 {
		newClass.PrimaryKeys = classConfig.PrimaryKeys
	}
	if classConfig.IDGenerator != "" {
		newClass.IDGenerator = classConfig.IDGenerator
	}
	if len(classConfig.Search) > 0 {
		newClass.Search = classConfig.Search
	}
	for _, s := range classConfig.Scope {
		newClass.Scope = append(newClass.Scope, protocol.ScopeRule(s)) // 字段同构，直接转换
	}
	my.applyFieldFilter(newClass, classConfig)
	if err := my.applyFieldConfig(newClass, classConfig.Fields); err != nil {
		return nil, err
	}
	// 主键列表缺省时从IsPrimary字段推导（与db加载器行为对齐）
	if len(newClass.PrimaryKeys) == 0 {
		for _, name := range utl.SortKeys(newClass.Fields) {
			if field := newClass.Fields[name]; name == field.Name && field.IsPrimary {
				newClass.PrimaryKeys = append(newClass.PrimaryKeys, name)
			}
		}
	}
	return newClass, nil
}

// 应用字段过滤
func (my *ConfigLoader) applyFieldFilter(class *protocol.Class, config *internal.ClassConfig) {
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
func (my *ConfigLoader) applyFieldConfig(class *protocol.Class, fieldConfigs map[string]*internal.FieldConfig) error {
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

	fields := class.Fields
	for _, fieldName := range orderedFields {
		fieldConfig := fieldConfigs[fieldName]

		// 字段已存在时沿用其实际列名；只用局部变量参与后续逻辑，
		// 绝不回写常驻的fieldConfig（配置对象跨次构建复用，写回会导致二次构建分组判定漂移）
		canonName := ConvertFieldName(fieldConfig.Column, config)
		column := fieldConfig.Column
		if field, ok := fields[fieldName]; ok {
			column = field.Column
		}

		// 虚拟字段
		if column == "" {
			fields[fieldName] = my.buildFieldFromConfig(class.Name, fieldName, column, fieldConfig, nil)
			continue
		}

		// 列字段、标准字段、覆盖字段统一处理
		if fieldName == column || fieldName == canonName || fieldConfig.Override {
			fields[column] = my.buildFieldFromConfig(class.Name, fieldName, column, fieldConfig, class.Fields[column])
			continue
		}

		// 别名字段（必须依赖基础字段）
		baseField, ok := fields[column]
		if !ok {
			return fmt.Errorf("别名字段 %s 必须有基础字段 %s", fieldName, column)
		}
		aliasField := clone.Slowly(baseField).(*protocol.Field)
		fields[fieldName] = my.buildFieldFromConfig(class.Name, fieldName, column, fieldConfig, aliasField)
	}
	return nil
}

// 字段创建或更新（类似类的处理方式）
// column为本次构建解析出的实际列名（可能来自已存在字段），与config.Column解耦
func (my *ConfigLoader) buildFieldFromConfig(className, fieldName, column string, config *internal.FieldConfig, baseField *protocol.Field) *protocol.Field {
	var field *protocol.Field
	if baseField != nil {
		field = baseField
	} else {
		field = &protocol.Field{}
	}
	field.Name = fieldName
	if column != "" || baseField == nil {
		field.Column = column
	}
	if config.Type != "" || baseField == nil {
		field.Type = config.Type
	}
	if config.Description != "" || baseField == nil {
		field.Description = config.Description
	}
	if baseField == nil || config.IsPrimary {
		field.IsPrimary = config.IsPrimary
	}
	if baseField == nil || config.IsUnique {
		field.IsUnique = config.IsUnique
	}
	if baseField == nil || config.IsNullable {
		field.Nullable = config.IsNullable
	}
	// 关系处理
	if config.Relation != nil {
		if field.Relation == nil {
			field.Relation = &protocol.Relation{
				SourceClass: className,
				SourceField: fieldName,
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
		if len(relConfig.SourceFields) > 1 { // 复合外键列组（首列冗余进单列字段，消费方统一经SourceColumns读取）
			rel.SourceFields, rel.SourceField = relConfig.SourceFields, relConfig.SourceFields[0]
			rel.TargetFields, rel.TargetField = relConfig.TargetFields, relConfig.TargetFields[0]
		}
		if relConfig.Type != "" {
			rel.Type = protocol.RelationType(relConfig.Type)
		}
		if relConfig.Through != nil {
			if rel.Through == nil {
				rel.Through = &protocol.Through{}
			}
			through := rel.Through
			throughConfig := relConfig.Through
			if throughConfig.TableName != "" {
				through.TableName = throughConfig.TableName
			}
			if throughConfig.SourceKey != "" {
				through.SourceKey = throughConfig.SourceKey
			}
			if throughConfig.TargetKey != "" {
				through.TargetKey = throughConfig.TargetKey
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
