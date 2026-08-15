package metadata

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/huandu/go-clone"
	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
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
		if classConfig == nil {
			return fmt.Errorf("配置坐标 metadata.classes.%s 为空", className)
		}
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
	orderedClasses := slices.Concat(tableClasses, canonClasses, overrideClasses, aliasClasses, virtualClasses)

	for _, className := range orderedClasses {
		classConfig := my.cfg.Metadata.Classes[className]
		if classConfig == nil {
			return fmt.Errorf("配置坐标 metadata.classes.%s 为空", className)
		}
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
	if classConfig.Global {
		newClass.Global = true
	}
	for _, s := range classConfig.Scope {
		newClass.Scope = append(newClass.Scope, protocol.ScopeRule(s)) // 字段同构，直接转换
	}
	my.applyFieldFilter(newClass, classConfig)
	if err := my.applyFieldConfig(newClass, classConfig.Fields); err != nil {
		return nil, err
	}
	if err := my.applyGovernance(newClass, classConfig); err != nil {
		return nil, err
	}
	if err := validateCatalogOverrides(baseClass, newClass, classConfig); err != nil {
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

func validateCatalogOverrides(base, class *protocol.Class, config *internal.ClassConfig) error {
	if base == nil || (len(base.Constraints) == 0 && len(base.Indexes) == 0) {
		return nil
	}
	columns := func(names []string) ([]string, error) {
		result := make([]string, len(names))
		for i, name := range names {
			field := class.Fields[name]
			if field == nil || field.Column == "" {
				return nil, fmt.Errorf("catalog覆盖引用未知字段 %s.%s", class.Name, name)
			}
			result[i] = field.Column
		}
		return result, nil
	}
	if len(config.PrimaryKeys) > 0 {
		configured, err := columns(config.PrimaryKeys)
		if err != nil {
			return err
		}
		var catalog []string
		for _, constraint := range base.Constraints {
			if constraint != nil && constraint.Kind == protocol.ConstraintPrimary {
				catalog = constraint.Columns
				break
			}
		}
		if !slices.Equal(configured, catalog) {
			return fmt.Errorf("catalog主键覆盖冲突 %s: 数据库=%v 配置=%v", class.Name, catalog, configured)
		}
	}
	for name, fieldConfig := range config.Fields {
		if fieldConfig == nil || !fieldConfig.IsUnique {
			continue
		}
		field := class.Fields[name]
		if field == nil {
			return fmt.Errorf("catalog unique覆盖引用未知字段 %s.%s", class.Name, name)
		}
		backed := false
		for _, key := range base.ConflictTargets() {
			if len(key.Columns) == 1 && key.Columns[0] == field.Column {
				backed = true
				break
			}
		}
		if !backed {
			return fmt.Errorf("catalog unique覆盖冲突 %s.%s: 数据库无可用唯一约束", class.Name, name)
		}
	}
	return nil
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
	for fieldName, fieldConfig := range fieldConfigs {
		if fieldConfig.InsertPreset != nil {
			if err := validateValue(fieldConfig.InsertPreset.Context, fieldConfig.InsertPreset.Value); err != nil {
				return fmt.Errorf("字段 %s insert-preset: %w", fieldName, err)
			}
		}
		if fieldConfig.UpdatePreset != nil {
			if err := validateValue(fieldConfig.UpdatePreset.Context, fieldConfig.UpdatePreset.Value); err != nil {
				return fmt.Errorf("字段 %s update-preset: %w", fieldName, err)
			}
		}
	}

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
	orderedFields := slices.Concat(tableFields, classFields, overrideFields, aliasFields, virtualFields)

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
			field := my.buildFieldFromConfig(class.Name, fieldName, column, fieldConfig, class.Fields[column])
			fields[column] = field
			if fieldName != column {
				fields[fieldName] = field
			}
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
	if baseField == nil || config.ReadOnly {
		field.ReadOnly = config.ReadOnly
	}
	if config.DefaultValue != "" {
		if field.Catalog == nil {
			field.Catalog = &protocol.Column{}
		}
		field.Catalog.Default = &protocol.ColumnDefault{Expression: config.DefaultValue}
	}
	if config.InsertPreset != nil {
		field.InsertPreset = &protocol.ValueSource{Context: config.InsertPreset.Context, Value: config.InsertPreset.Value}
	}
	if config.UpdatePreset != nil {
		field.UpdatePreset = &protocol.ValueSource{Context: config.UpdatePreset.Context, Value: config.UpdatePreset.Value}
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
		if len(relConfig.SourceFields) > 0 { // 显式列组覆盖单列字段；首列冗余供旧消费方读取
			rel.SourceFields, rel.SourceField = relConfig.SourceFields, relConfig.SourceFields[0]
			rel.TargetFields = relConfig.TargetFields
			if len(rel.TargetFields) > 0 {
				rel.TargetField = rel.TargetFields[0]
			}
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

func validateValue(context string, value any) error {
	if context != "" && value != nil {
		return fmt.Errorf("value同时设置context和value")
	}
	if context == "" && value == nil {
		return fmt.Errorf("value必须设置context或value")
	}
	return nil
}

func validatePredicate(p internal.PredicateConfig) error {
	switch p.Operator {
	case "eq", "ne":
		if err := validateValue(p.Context, p.Value); err != nil {
			return err
		}
	case "is-null", "not-null":
		if p.Context != "" || p.Value != nil {
			return fmt.Errorf("%s谓词不能带值", p.Operator)
		}
	default:
		return fmt.Errorf("未知谓词操作符 %q", p.Operator)
	}
	return nil
}

func (my *ConfigLoader) resolveColumn(class *protocol.Class, name string) (*protocol.Field, error) {
	field, ok := class.Fields[name]
	if !ok {
		for _, candidate := range class.Fields {
			if candidate != nil && candidate.Name == name {
				field, ok = candidate, true
				break
			}
		}
	}
	if !ok || field == nil || field.Column == "" {
		return nil, fmt.Errorf("未知字段或列 %q", name)
	}
	return field, nil
}

func (my *ConfigLoader) resolvePredicate(class *protocol.Class, p internal.PredicateConfig) (protocol.Predicate, error) {
	if err := validatePredicate(p); err != nil {
		return protocol.Predicate{}, err
	}
	field, err := my.resolveColumn(class, p.Column)
	if err != nil {
		return protocol.Predicate{}, err
	}
	return protocol.Predicate{Column: field.Column, Operator: p.Operator, Context: p.Context, Value: p.Value}, nil
}

func (my *ConfigLoader) resolvePredicates(class *protocol.Class, configs []internal.PredicateConfig) ([]protocol.Predicate, error) {
	result := make([]protocol.Predicate, 0, len(configs))
	for _, config := range configs {
		p, err := my.resolvePredicate(class, config)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, nil
}

func samePredicate(a, b protocol.Predicate) bool {
	return a.Column == b.Column && a.Operator == b.Operator && a.Context == b.Context && reflect.DeepEqual(a.Value, b.Value)
}

func (my *ConfigLoader) resolvePermissionFields(class *protocol.Class, names []string) ([]string, error) {
	if len(names) == 1 && names[0] == "*" {
		return []string{"*"}, nil
	}
	result := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		field, err := my.resolveColumn(class, name)
		if err != nil {
			return nil, err
		}
		if !seen[field.Name] {
			result = append(result, field.Name)
			seen[field.Name] = true
		}
	}
	return result, nil
}

func (my *ConfigLoader) applyGovernance(class *protocol.Class, config *internal.ClassConfig) error {
	if len(config.Operations) > 0 {
		allowed := map[string]bool{"select": true, "aggregate": true, "insert": true, "update": true, "delete": true, "restore": true, "purge": true}
		seen := make(map[string]bool)
		class.Operations = nil
		for _, operation := range config.Operations {
			if !allowed[operation] {
				return fmt.Errorf("未知操作 %q", operation)
			}
			if !seen[operation] {
				class.Operations = append(class.Operations, operation)
				seen[operation] = true
			}
		}
	}
	appendPredicate := func(predicate protocol.Predicate) {
		for _, existing := range class.Where {
			if samePredicate(existing, predicate) {
				return
			}
		}
		class.Where = append(class.Where, predicate)
	}
	if config.SoftDelete != nil {
		field, err := my.resolveColumn(class, config.SoftDelete.Column)
		if err != nil {
			return fmt.Errorf("soft-delete: %w", err)
		}
		class.SoftDelete = &protocol.SoftDelete{Column: field.Column}
		appendPredicate(protocol.Predicate{Column: field.Column, Operator: "is-null"})
	}
	where, err := my.resolvePredicates(class, config.Where)
	if err != nil {
		return fmt.Errorf("where: %w", err)
	}
	for _, p := range where {
		appendPredicate(p)
	}
	if config.Permissions == nil {
		return nil
	}
	if class.Permissions == nil {
		class.Permissions = make(map[string]*protocol.Permission)
	}
	for role, pc := range config.Permissions {
		if pc == nil {
			return fmt.Errorf("permission %s 不能为空", role)
		}
		perm := &protocol.Permission{Delete: pc.Delete, Restore: pc.Restore, Purge: pc.Purge, Aggregate: pc.Aggregate}
		var err error
		if perm.Select, err = my.resolvePermissionFields(class, pc.Select); err != nil {
			return fmt.Errorf("permission %s select: %w", role, err)
		}
		if perm.Insert, err = my.resolvePermissionFields(class, pc.Insert); err != nil {
			return fmt.Errorf("permission %s insert: %w", role, err)
		}
		if perm.Update, err = my.resolvePermissionFields(class, pc.Update); err != nil {
			return fmt.Errorf("permission %s update: %w", role, err)
		}
		if perm.Filter, err = my.resolvePredicates(class, pc.Filter); err != nil {
			return fmt.Errorf("permission %s filter: %w", role, err)
		}
		if perm.Check, err = my.resolvePredicates(class, pc.Check); err != nil {
			return fmt.Errorf("permission %s check: %w", role, err)
		}
		class.Permissions[role] = perm
	}
	return nil
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
