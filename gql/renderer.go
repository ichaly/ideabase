package gql

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/jinzhu/inflection"
)

// 分隔线和描述常量
const (
	// 分隔线
	SEPARATOR_LINE = "------------------"

	// 描述性文本
	DESC_SCHEMA_TITLE = "IdeaBase GraphQL Schema"
	DESC_SCALAR_TYPES = "自定义标量类型"
	DESC_SORT_ENUM    = "排序方向枚举，包含NULL值处理"
	DESC_IS_ENUM      = "空值条件枚举"

	// 分类标题
	SECTION_FILTER     = "过滤器类型定义"
	SECTION_QUERY      = "查询和变更"
	SECTION_CONNECTION = "连接和边类型（游标分页）"
)

// Renderer 负责将元数据渲染为GraphQL schema
type Renderer struct {
	meta *Metadata
	sb   *strings.Builder
}

// NewRenderer 创建新的Schema渲染器
func NewRenderer(meta *Metadata) *Renderer {
	if meta.operations == nil {
		meta.operations = buildOperationCatalog(meta)
	}
	return &Renderer{
		meta: meta,
		sb:   &strings.Builder{},
	}
}

// Render 纯函数式生成完整GraphQL schema，不写文件。
func (my *Renderer) Render() (string, error) {
	// 复用构造时分配的构建器（支持重复 Render）
	my.sb.Reset()
	// 添加schema版本和说明
	my.writeLine("# ", DESC_SCHEMA_TITLE)
	my.writeLine("# 版本: ", my.meta.Version, "\n")

	my.renderScalars()
	if err := my.renderEnums(); err != nil {
		return "", fmt.Errorf("渲染枚举类型失败: %w", err)
	}
	my.renderTypes()
	my.renderPaging()
	my.renderStats()
	my.renderFilter()
	my.renderEntity()
	my.renderSort()
	my.renderInput()
	my.renderQuery()
	my.renderMutation()

	return my.sb.String(), nil
}

// writeLine 写入一行文本（自动添加换行符）
// 支持可变参数，避免字符串相加操作，提高性能
func (my *Renderer) writeLine(parts ...string) {
	my.write(parts...)
	my.write("\n")
}

// write 直接写入文本
// 支持可变参数，避免字符串相加操作，提高性能
func (my *Renderer) write(parts ...string) {
	for _, part := range parts {
		my.sb.WriteString(part)
	}
}

// renderScalars 渲染标量类型（含codec带来的自定义标量，如Phone）
func (my *Renderer) renderScalars() {
	my.writeLine("# ", DESC_SCALAR_TYPES)
	my.writeLine("scalar ", protocol.SCALAR_JSON)
	my.writeLine("scalar ", protocol.SCALAR_CURSOR)
	my.writeLine("scalar ", protocol.SCALAR_DATE_TIME)
	my.writeLine("scalar ", protocol.SCALAR_BIG_INT)
	my.writeLine("scalar ", protocol.SCALAR_DECIMAL)
	my.eachCodecScalar(func(name, _ string) {
		my.writeLine("scalar ", name)
	})
	my.writeLine()
}

// eachCodecScalar 遍历codec引入的自定义标量（跳过内置标量如ID，同名去重），
// fn收到标量名与其底层标量名（codec未实现Baser时默认借用String）
func (my *Renderer) eachCodecScalar(fn func(name, base string)) {
	seen := make(map[string]bool, len(my.meta.codecs))
	for _, codec := range my.meta.codecs {
		name := codec.Name()
		if protocol.IsBuiltinScalar(name) || seen[name] {
			continue
		}
		seen[name] = true
		base := protocol.SCALAR_STRING
		if b, ok := codec.(Baser); ok {
			base = b.Base()
		}
		fn(name, base)
	}
}

// renderEnums 渲染枚举类型
func (my *Renderer) renderEnums() error {
	// 渲染排序方向枚举
	my.writeLine("# ", DESC_SORT_ENUM)
	my.writeLine("enum ", protocol.TYPE_SORT_DIRECTION, " {")
	my.writeLine("  ASC")
	my.writeLine("  DESC")
	my.writeLine("  ASC_NULLS_FIRST")
	my.writeLine("  DESC_NULLS_FIRST")
	my.writeLine("  ASC_NULLS_LAST")
	my.writeLine("  DESC_NULLS_LAST")
	my.writeLine("}")
	my.writeLine()

	// 渲染空值条件枚举
	my.writeLine("# ", DESC_IS_ENUM)
	my.writeLine("enum IsInput {")
	my.writeLine("  NULL")
	my.writeLine("  NOT_NULL")
	my.writeLine("}")
	my.writeLine()
	hasSoftDelete := false
	my.eachTableClass(func(_ string, class *protocol.Class) { hasSoftDelete = hasSoftDelete || class.SoftDelete != nil })
	if hasSoftDelete {
		my.writeLine("enum ", protocol.TYPE_DELETION_MODE, " {")
		my.writeLine("  ACTIVE")
		my.writeLine("  WITH_DELETED")
		my.writeLine("  ONLY_DELETED")
		my.writeLine("}")
		my.writeLine()
	}

	// Database enums are emitted only when labels are losslessly representable
	// by GraphQL. Unsafe labels fail schema build instead of being silently
	// rewritten to values that PostgreSQL would not accept.
	enums := make(map[string][]string)
	var enumErr error
	my.eachTableClass(func(_ string, class *protocol.Class) {
		my.eachField(class, func(_ string, field *protocol.Field) {
			if ref := my.catalogType(field); ref != nil {
				if ref.Kind == protocol.TypeArray {
					ref = ref.Element
				}
				if ref != nil && ref.Kind == protocol.TypeEnum {
					name := my.enumName(ref)
					if labels, exists := enums[name]; exists && !slices.Equal(labels, ref.EnumLabels) {
						enumErr = fmt.Errorf("PostgreSQL enum映射冲突: %s", name)
						return
					}
					enums[name] = ref.EnumLabels
				}
			}
		})
	})
	if enumErr != nil {
		return enumErr
	}
	for _, name := range slices.Sorted(maps.Keys(enums)) {
		if !graphQLName(name) {
			return fmt.Errorf("PostgreSQL enum类型名无法无损映射: %q", name)
		}
		my.writeLine("enum ", name, " {")
		for _, label := range enums[name] {
			if !graphQLName(label) || label == "true" || label == "false" || label == "null" {
				return fmt.Errorf("PostgreSQL enum %s 含非法GraphQL值 %q，请使用显式类型映射", name, label)
			}
			my.writeLine("  ", label)
		}
		my.writeLine("}")
		my.writeLine()
	}
	my.eachTableClass(func(className string, class *protocol.Class) {
		writeColumns := func(suffix string) {
			my.writeLine("enum ", className, suffix, " {")
			my.eachField(class, func(name string, field *protocol.Field) {
				if field.Column != "" && !my.hiddenField(field) {
					my.writeLine("  ", name)
				}
			})
			my.writeLine("}")
			my.writeLine()
		}
		if catalogAllows(class, protocol.OP_SELECT) {
			writeColumns(protocol.SUFFIX_DISTINCT_FIELD)
		}
		if catalogAllows(class, protocol.OP_AGGREGATE) {
			writeColumns(protocol.SUFFIX_GROUP_FIELD)
		}
		keys := class.UpsertConflictTargets()
		if len(keys) == 0 {
			return
		}
		my.writeLine("enum ", className, "ConflictTarget {")
		for _, key := range keys {
			my.writeLine("  ", key.Name, "  # ", strings.Join(key.Columns, ", "))
		}
		my.writeLine("}")
		my.writeLine()
	})

	return nil
}

// eachClass 遍历可见实体类（跳过表名索引与隐藏的中间表），按类名有序
func (my *Renderer) eachClass(fn func(className string, class *protocol.Class)) {
	for _, className := range slices.Sorted(maps.Keys(my.meta.Nodes)) {
		class := my.meta.Nodes[className]
		if className != class.Name || class.Hidden || (class.IsThrough && !my.meta.cfg.Metadata.ShowThrough) {
			continue
		}
		fn(className, class)
	}
}

// eachTableClass 仅遍历有表实体类：虚拟类（远程关系目标/纯Resolver类）只有类型定义，
// 不生成查询根/变更/过滤/排序/输入/分页等查询面（无SQL能力，渲染即虚假展示）
func (my *Renderer) eachTableClass(fn func(className string, class *protocol.Class)) {
	my.eachClass(func(className string, class *protocol.Class) {
		if class.Table != "" {
			fn(className, class)
		}
	})
}

// eachField 按名有序遍历类的主名字段（跳过列名索引）
func (my *Renderer) eachField(class *protocol.Class, fn func(fieldName string, field *protocol.Field)) {
	for _, fieldName := range slices.Sorted(maps.Keys(class.Fields)) {
		if field := class.Fields[fieldName]; fieldName == field.Name {
			fn(fieldName, field)
		}
	}
}

// hiddenField 字段是否随中间表隐藏：自身是中间表字段，或引用了隐藏的中间表类型
func (my *Renderer) hiddenField(field *protocol.Field) bool {
	return fieldHidden(my.meta, field)
}

// renderTypes 渲染所有实体类型定义
func (my *Renderer) renderTypes() {
	my.eachClass(func(className string, class *protocol.Class) {
		// 添加类型描述
		if class.Description != "" {
			my.writeLine("# ", class.Description)
		}

		// 开始类型定义
		my.writeLine("type ", className, " {")

		// 添加所有字段：跳过列名索引与随中间表隐藏的字段
		my.eachField(class, func(fieldName string, field *protocol.Field) {
			if my.hiddenField(field) {
				return
			}

			// 添加描述作为注释
			if field.Description != "" {
				my.writeLine("  # ", field.Description)
			}

			// 获取GraphQL字段类型
			typeName := my.getGraphQLType(field)

			// 处理非空标记
			if !field.Nullable {
				typeName += "!"
			}

			// 列表关系字段支持嵌套过滤/排序/分页参数；深度递归字段附加depth限深
			if field.Column == "" && field.Relation != nil && field.IsList {
				target := field.Relation.TargetClass
				args := []Argument{
					{Name: protocol.WHERE, Type: target + protocol.SUFFIX_WHERE_INPUT},
					{Name: protocol.SORT, Type: "[" + target + protocol.SUFFIX_SORT_INPUT + "!]"},
					{Name: protocol.LIMIT, Type: protocol.SCALAR_INT},
				}
				if targetClass := my.meta.Nodes[target]; targetClass != nil {
					if len(targetClass.PrimaryKeyFields()) > 0 {
						args = append(args, Argument{Name: protocol.OFFSET, Type: protocol.SCALAR_INT})
					}
					if targetClass.SoftDelete != nil {
						args = append(args, Argument{Name: protocol.DELETION, Type: protocol.TYPE_DELETION_MODE, Default: "ACTIVE"})
					}
				}
				if field.Relation.Deep {
					args = append(args, Argument{Name: protocol.DEPTH, Type: protocol.SCALAR_INT})
				}
				my.writeField(fieldName, typeName, "", args...)
				return
			}

			// 输出字段定义
			my.writeLine("  ", fieldName, ": ", typeName)
		})

		// 结束类型定义
		my.writeLine("}")
		my.writeLine()
	})

}

// getGraphQLType 字段元数据类型转GraphQL类型名：
// 数据库原生类型经映射表转换；标量/类名/codec标量在元数据定型期已是最终名，原样返回
// （映射表key全为小写数据库类型名，与标量/类名不冲突；空类型是元数据bug，交由schema解析报错暴露）
func (my *Renderer) getGraphQLType(field *protocol.Field) string {
	if field.IsList {
		// 列表字段仅由关系处理生成，元素类型即关系目标类名
		return "[" + field.Type + "]"
	}
	if ref := my.catalogType(field); ref != nil {
		return my.catalogGraphQLType(ref)
	}
	if gqlType, ok := my.meta.cfg.Schema.TypeMapping[field.Type]; ok {
		return gqlType
	}
	return field.Type
}

func (my *Renderer) catalogType(field *protocol.Field) *protocol.TypeRef {
	if field.Catalog == nil {
		return nil
	}
	ref := &field.Catalog.Type
	// A configured type, primary/foreign ID finalization, or Codec is the final
	// explicit verdict and must not be overwritten by raw catalog facts.
	if field.Type != ref.Name && field.Type != ref.Base {
		return nil
	}
	return ref
}

func (my *Renderer) catalogGraphQLType(ref *protocol.TypeRef) string {
	switch ref.Kind {
	case protocol.TypeArray:
		if ref.Element == nil {
			return protocol.SCALAR_JSON
		}
		return "[" + my.catalogGraphQLType(ref.Element) + "]"
	case protocol.TypeEnum:
		return my.enumName(ref)
	case protocol.TypeDomain:
		if mapped, ok := my.meta.cfg.Schema.TypeMapping[ref.Base]; ok {
			return mapped
		}
	}
	if mapped, ok := my.meta.cfg.Schema.TypeMapping[ref.Name]; ok {
		return mapped
	}
	return ref.Name
}

func (*Renderer) enumName(ref *protocol.TypeRef) string { return strcase.ToCamel(ref.Name) + "Enum" }

func graphQLName(name string) bool {
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return name != ""
}

func auditColumn(column string) bool {
	switch column {
	case "created_at", "created_by", "updated_at", "updated_by", "deleted_at", "deleted_by":
		return true
	}
	return false
}

// writableFields 返回类的可写字段名（排除主键、时间戳、虚拟与中间表字段）
// writeRelationOps 输入类型中的列表关系操作字段（connect/disconnect原子挂接）
func (my *Renderer) writeRelationOps(class *protocol.Class, operation string) {
	my.eachField(class, func(fieldName string, field *protocol.Field) {
		// 仅列表关系虚拟字段（一对多/多对多），中间表隐藏时跳过
		if field.Column == "" && field.Relation != nil && !field.Relation.Composite() && field.IsList && !my.hiddenField(field) {
			target := my.meta.Nodes[field.Relation.TargetClass]
			if target == nil || !my.relationInputAllowed(target) {
				return
			}
			suffix := protocol.SUFFIX_UPDATE_RELATION_INPUT
			if operation == protocol.OP_INSERT {
				suffix = protocol.SUFFIX_CREATE_RELATION_INPUT
			}
			my.writeField(fieldName, field.Relation.TargetClass+suffix, "")
		}
	})
}

func (my *Renderer) writeMutationInput(className, suffix string, class *protocol.Class, fields []string, operation string, relations bool) {
	my.writeLine("input ", className, suffix, " {")
	for _, fieldName := range fields {
		field := class.Fields[fieldName]
		typeName := my.getGraphQLType(field)
		if operation == protocol.OP_INSERT && !field.Nullable && field.Relation == nil && (field.Catalog == nil || field.Catalog.Default == nil) {
			typeName += "!"
		}
		my.writeField(fieldName, typeName, "")
	}
	if relations {
		my.writeRelationOps(class, operation)
	}
	my.writeLine("}")
	my.writeLine()
}

// renderInput 渲染输入类型
func (my *Renderer) renderInput() {
	// 为每个实体类生成创建和更新输入类型
	my.eachTableClass(func(className string, class *protocol.Class) {
		primaryKeys := class.PrimaryKeyFields()
		if len(primaryKeys) > 1 {
			my.writeLine("# ", className, "复合主键")
			my.writeLine("input ", className, protocol.SUFFIX_KEY_INPUT, " {")
			for _, column := range primaryKeys {
				if field := class.Fields[column]; field != nil {
					my.writeField(field.Name, my.getGraphQLType(field)+"!", "")
				}
			}
			my.writeLine("}")
			my.writeLine()
		}
		// 无可写字段的类（如纯主键表）不生成输入类型
		createFields := writableFields(my.meta, class, protocol.OP_INSERT)
		updateFields := writableFields(my.meta, class, protocol.OP_UPDATE)
		plural := inflection.Plural(className)
		batch := mutationBatchName(className)
		createOperation := my.operationExists("Mutation", protocol.CREATE+className) ||
			my.operationExists("Mutation", protocol.CREATE+plural)
		singleUpdate := my.operationExists("Mutation", protocol.UPDATE+className)
		batchUpdate := my.operationExists("Mutation", protocol.UPDATE+batch)
		if createOperation && len(createFields) > 0 {
			my.writeLine("# ", className, "创建输入")
			my.writeMutationInput(className, protocol.SUFFIX_CREATE_INPUT, class, createFields, protocol.OP_INSERT, true)
			my.writeMutationInput(className, protocol.SUFFIX_CREATE_ROW_INPUT, class, createFields, protocol.OP_INSERT, false)
		}
		if my.operationExists("Mutation", protocol.UPSERT+plural) && len(createFields) > 0 && len(class.UpsertConflictTargets()) > 0 {
			upsertFields := slices.DeleteFunc(append([]string(nil), createFields...), func(name string) bool {
				return !slices.Contains(updateFields, name)
			})
			if len(upsertFields) > 0 {
				my.writeMutationInput(className, protocol.SUFFIX_UPSERT_ROW_INPUT, class, upsertFields, protocol.OP_INSERT, false)
			}
		}

		if singleUpdate && len(updateFields) > 0 {
			my.writeLine("# ", className, "更新输入")
			my.writeMutationInput(className, protocol.SUFFIX_UPDATE_INPUT, class, updateFields, protocol.OP_UPDATE, true)
		}
		if batchUpdate && len(updateFields) > 0 {
			my.writeMutationInput(className, protocol.SUFFIX_UPDATE_ROW_INPUT, class, updateFields, protocol.OP_UPDATE, false)
		}
	})

	// 按目标类生成关系操作输入：connect/disconnect挂接解除既有行，create内联建新行
	my.eachTableClass(func(className string, class *protocol.Class) {
		if !my.relationInputAllowed(class) {
			return
		}
		connectable := my.hasMutationOperation(class, protocol.OP_UPDATE)
		creatable := my.hasMutationOperation(class, protocol.OP_INSERT)
		if my.relationInputUsed(className, protocol.OP_INSERT) {
			my.writeLine("# ", className, "创建关系操作")
			my.writeLine("input ", className, protocol.SUFFIX_CREATE_RELATION_INPUT, " {")
			if connectable {
				my.writeField("connect", "["+protocol.SCALAR_ID+"!]", "挂接既有行主键")
			}
			if creatable {
				my.writeField("create", "["+className+protocol.SUFFIX_CREATE_ROW_INPUT+"!]", "内联创建新行")
			}
			my.writeLine("}")
			my.writeLine()
		}
		if my.relationInputUsed(className, protocol.OP_UPDATE) {
			my.writeLine("# ", className, "更新关系操作")
			my.writeLine("input ", className, protocol.SUFFIX_UPDATE_RELATION_INPUT, " {")
			if connectable {
				my.writeField("connect", "["+protocol.SCALAR_ID+"!]", "挂接既有行主键")
				my.writeField("disconnect", "["+protocol.SCALAR_ID+"!]", "解除既有行主键")
			}
			if creatable {
				my.writeField("create", "["+className+protocol.SUFFIX_CREATE_ROW_INPUT+"!]", "内联创建新行")
			}
			my.writeLine("}")
			my.writeLine()
		}
	})

}

func (my *Renderer) relationInputUsed(target, operation string) bool {
	used := false
	my.eachTableClass(func(_ string, class *protocol.Class) {
		if used || !my.hasMutationOperation(class, operation) || len(writableFields(my.meta, class, operation)) == 0 {
			return
		}
		my.eachField(class, func(_ string, field *protocol.Field) {
			used = used || field.IsList && field.Relation != nil && !field.Relation.Composite() && field.Relation.TargetClass == target && !my.hiddenField(field)
		})
	})
	return used
}

func (my *Renderer) relationInputAllowed(class *protocol.Class) bool {
	return len(class.PrimaryKeyFields()) == 1 && (my.hasMutationOperation(class, protocol.OP_UPDATE) ||
		my.hasMutationOperation(class, protocol.OP_INSERT))
}

func (my *Renderer) operationExists(parent, name string) bool {
	_, ok := my.meta.operations[parent+"."+name]
	return ok
}

func (my *Renderer) hasMutationOperation(class *protocol.Class, operation string) bool {
	if class == nil {
		return false
	}
	name := protocol.UPDATE + class.Name
	if operation == protocol.OP_INSERT {
		name = protocol.CREATE + class.Name
	}
	return my.operationExists("Mutation", name)
}

// renderFilter 渲染基础过滤器类型
func (my *Renderer) renderFilter() {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_FILTER, " ", SEPARATOR_LINE, "\n")

	// 内置标量按操作符分组渲染；codec自定义标量借用其底层标量的操作符集
	for _, scalarType := range protocol.ScalarTypes() {
		my.writeScalarFilter(scalarType, protocol.OperatorsForScalar(scalarType))
	}
	my.eachCodecScalar(func(name, base string) {
		my.writeScalarFilter(name, protocol.OperatorsForScalar(base))
	})
}

// writeScalarFilter 渲染一个标量的过滤器input类型
func (my *Renderer) writeScalarFilter(scalarType string, operators []*protocol.Operator) {
	my.writeLine("# ", scalarType, "过滤器")
	my.writeLine("input ", scalarType, protocol.SUFFIX_WHERE_INPUT, " {")

	// 使用map防止操作符重复
	renderedOps := make(map[string]bool)
	for _, op := range operators {
		if renderedOps[op.Name] {
			continue
		}
		renderedOps[op.Name] = true

		switch op.Name {
		case protocol.HAS_KEY:
			my.writeField(op.Name, protocol.SCALAR_STRING, op.Description)
		case protocol.IN:
			my.writeField(op.Name, "["+scalarType+"!]", op.Description)
		case protocol.IS:
			my.writeField(op.Name, protocol.ENUM_IS_INPUT, op.Description)
		default:
			my.writeField(op.Name, scalarType, op.Description)
		}
	}

	my.writeLine("}")
	my.writeLine()
}

// renderEntity 渲染实体过滤器
func (my *Renderer) renderEntity() {
	// 为每个实体类生成过滤器
	my.eachTableClass(func(className string, class *protocol.Class) {
		// 生成过滤器类型
		my.writeLine("# ", className, "查询条件")
		my.writeLine("input ", className, protocol.SUFFIX_WHERE_INPUT, " {")

		// 添加常规字段过滤条件：跳过列名索引/虚拟关系字段/随中间表隐藏的字段
		my.eachField(class, func(fieldName string, field *protocol.Field) {
			ref := my.catalogType(field)
			if !field.Virtual && !my.hiddenField(field) && (ref == nil || ref.Kind != protocol.TypeArray && ref.Kind != protocol.TypeEnum) {
				my.writeLine("  ", fieldName, ": ", my.getGraphQLType(field), protocol.SUFFIX_WHERE_INPUT)
			}
		})

		// 添加布尔逻辑操作符
		my.writeLine("  and: [", className, protocol.SUFFIX_WHERE_INPUT, "!]")
		my.writeLine("  or: [", className, protocol.SUFFIX_WHERE_INPUT, "!]")
		my.writeLine("  not: ", className, protocol.SUFFIX_WHERE_INPUT)

		my.writeLine("}")
		my.writeLine("")
	})

}

// renderSort 渲染排序类型
func (my *Renderer) renderSort() {
	// 为每个实体类生成排序类型
	my.eachTableClass(func(className string, class *protocol.Class) {
		// 生成排序类型
		my.writeLine("# ", className, "排序")
		my.writeLine("input ", className, protocol.SUFFIX_SORT_INPUT, " {")

		// 添加可排序字段：跳过列名索引、全部虚拟字段（关系载体/resolver/remote
		// 都没有可排序的物理列，渲染进SortInput即运行期SQL必错的虚假展示）
		// 与随中间表隐藏的字段
		my.eachField(class, func(fieldName string, field *protocol.Field) {
			if !field.Virtual && !my.hiddenField(field) {
				my.writeField(fieldName, protocol.TYPE_SORT_DIRECTION, "")
			}
		})

		my.writeLine("}")
		my.writeLine("")
	})

}

// renderQuery 渲染查询根类型与订阅根类型（订阅镜像实体查询字段）
func (my *Renderer) renderQuery() {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_QUERY, " ", SEPARATOR_LINE, "\n")
	operationExists := my.operationExists

	// 实体查询字段渲染闭包，Query与Subscription共用
	writeEntityField := func(className string) {
		args := []Argument{}
		// 声明了搜索列的实体提供全文搜索参数
		if class := my.meta.Nodes[className]; class != nil && len(class.Search) > 0 {
			args = append(args, Argument{Name: protocol.SEARCH, Type: protocol.SCALAR_STRING})
		}
		my.writeLine("  # ", className, "查询")
		class := my.meta.Nodes[className]
		if class.SoftDelete != nil {
			args = append(args, Argument{Name: protocol.DELETION, Type: protocol.TYPE_DELETION_MODE, Default: "ACTIVE"})
		}
		primaryKeys := class.PrimaryKeyFields()
		if len(primaryKeys) == 1 {
			args = append(args, Argument{Name: protocol.ID, Type: protocol.SCALAR_ID})
		} else if len(primaryKeys) > 1 {
			args = append(args, Argument{Name: protocol.KEY, Type: className + protocol.SUFFIX_KEY_INPUT})
		}
		paging := []Argument{
			{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT},
			{Name: protocol.SORT, Type: "[" + className + protocol.SUFFIX_SORT_INPUT + "!]"},
			{Name: protocol.DISTINCT, Type: "[" + className + protocol.SUFFIX_DISTINCT_FIELD + "!]"},
			{Name: protocol.LIMIT, Type: protocol.SCALAR_INT},
		}
		if len(primaryKeys) > 0 {
			paging = append(paging,
				Argument{Name: protocol.OFFSET, Type: protocol.SCALAR_INT},
				Argument{Name: protocol.FIRST, Type: protocol.SCALAR_INT},
				Argument{Name: protocol.AFTER, Type: protocol.SCALAR_CURSOR},
				Argument{Name: protocol.LAST, Type: protocol.SCALAR_INT},
				Argument{Name: protocol.BEFORE, Type: protocol.SCALAR_CURSOR},
			)
		}
		my.writeField(queryField(className), className+protocol.SUFFIX_RESULT+"!", "", append(args, paging...)...)
	}
	writeConnectionField := func(className string) {
		class := my.meta.Nodes[className]
		if len(class.PrimaryKeyFields()) == 0 {
			return
		}
		args := []Argument{
			{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT},
			{Name: protocol.SORT, Type: "[" + className + protocol.SUFFIX_SORT_INPUT + "!]"},
			{Name: protocol.FIRST, Type: protocol.SCALAR_INT},
			{Name: protocol.AFTER, Type: protocol.SCALAR_CURSOR},
			{Name: protocol.LAST, Type: protocol.SCALAR_INT},
			{Name: protocol.BEFORE, Type: protocol.SCALAR_CURSOR},
		}
		if len(class.Search) > 0 {
			args = append([]Argument{{Name: protocol.SEARCH, Type: protocol.SCALAR_STRING}}, args...)
		}
		if class.SoftDelete != nil {
			args = append(args, Argument{Name: protocol.DELETION, Type: protocol.TYPE_DELETION_MODE, Default: "ACTIVE"})
		}
		my.writeField(strcase.ToLowerCamel(className)+protocol.SUFFIX_CONNECTION, className+protocol.SUFFIX_CONNECTION+"!", "", args...)
	}

	// 收集可渲染的实体类名
	var names []string
	var selectNames []string
	my.eachTableClass(func(className string, _ *protocol.Class) {
		if operationExists("Query", queryField(className)) {
			selectNames = append(selectNames, className)
		}
		if operationExists("Query", queryField(className)) || operationExists("Query", strcase.ToLowerCamel(className)+protocol.SUFFIX_STATS) {
			names = append(names, className)
		}
	})

	my.writeLine("# 查询根类型")
	my.writeLine("type Query {")
	for _, className := range names {
		class := my.meta.Nodes[className]
		if operationExists("Query", queryField(className)) {
			writeEntityField(className)
			if operationExists("Query", strcase.ToLowerCamel(className)+protocol.SUFFIX_CONNECTION) {
				writeConnectionField(className)
			}
		}

		// 统计查询
		if !operationExists("Query", strcase.ToLowerCamel(className)+protocol.SUFFIX_STATS) {
			continue
		}
		my.writeLine("  # ", className, "统计")
		statsArgs := []Argument{
			{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT},
			{Name: protocol.GROUP_BY, Type: "[" + className + protocol.SUFFIX_GROUP_FIELD + "!]"},
			{Name: protocol.HAVING, Type: className + protocol.SUFFIX_HAVING_INPUT},
			{Name: protocol.LIMIT, Type: protocol.SCALAR_INT},
			{Name: protocol.OFFSET, Type: protocol.SCALAR_INT},
		}
		if class.SoftDelete != nil {
			statsArgs = append(statsArgs, Argument{Name: protocol.DELETION, Type: protocol.TYPE_DELETION_MODE, Default: "ACTIVE"})
		}
		my.writeField(strcase.ToLowerCamel(className)+protocol.SUFFIX_STATS, "["+className+protocol.SUFFIX_STATS+"!]"+"!", "", statsArgs...)
	}
	if len(names) == 0 {
		my.writeLine("  _empty: Boolean")
	}
	my.writeLine("}")
	my.writeLine()

	// 订阅根类型：轮询式订阅，能力与实体查询一致
	my.writeLine("# 订阅根类型")
	my.writeLine("type Subscription {")
	for _, className := range names {
		if operationExists("Subscription", queryField(className)) {
			writeEntityField(className)
			if operationExists("Subscription", strcase.ToLowerCamel(className)+protocol.SUFFIX_CONNECTION) {
				writeConnectionField(className)
			}
		}
	}
	if len(selectNames) == 0 {
		my.writeLine("  _empty: Boolean")
	}
	my.writeLine("}")
	my.writeLine()
}

// renderMutation 渲染变更根类型
func (my *Renderer) renderMutation() {
	operationExists := func(name string) bool { return my.operationExists("Mutation", name) }
	hasMutation := false
	for coordinate := range my.meta.operations {
		if strings.HasPrefix(coordinate, "Mutation.") {
			hasMutation = true
			break
		}
	}
	if !hasMutation {
		return
	}
	my.eachTableClass(func(className string, class *protocol.Class) {
		batch := mutationBatchName(className)
		if !operationExists(protocol.UPDATE+className) && !operationExists(protocol.DELETE+className) && !operationExists("restore"+className+protocol.SUFFIX_BY_PK) && !operationExists(protocol.PURGE+className+protocol.SUFFIX_BY_PK) &&
			!operationExists(protocol.UPDATE+batch) && !operationExists(protocol.DELETE+batch) &&
			!operationExists("restore"+batch) && !operationExists(protocol.PURGE+batch) {
			return
		}
		my.writeLine("type ", className, protocol.SUFFIX_MUTATION_RESULT, " {")
		my.writeField("affectedCount", protocol.SCALAR_INT+"!", "")
		my.writeField("returning", "["+className+"!]"+"!", "")
		my.writeLine("}")
		my.writeLine()
	})
	my.writeLine("# 突变根类型")
	my.writeLine("type Mutation {")

	// 按排序顺序渲染每种类型的变更操作
	my.eachTableClass(func(className string, class *protocol.Class) {

		// 无可写字段的类（如纯主键表）不生成创建/更新操作
		plural := inflection.Plural(className)
		if operationExists(protocol.CREATE + className) {
			my.writeLine("  # ", class.Name, "创建")
			args := []Argument(nil)
			if len(writableFields(my.meta, class, protocol.OP_INSERT)) > 0 {
				args = []Argument{{Name: protocol.INPUT, Type: className + protocol.SUFFIX_CREATE_INPUT + "!"}}
			}
			my.writeField(protocol.CREATE+className, className+"!", "", args...)
		}
		// 不可数类名（Series/News）复数同形，catalog不会生成批量字段。
		if plural != className && operationExists(protocol.CREATE+plural) {
			my.writeLine("  # ", class.Name, "批量创建")
			my.writeField(protocol.CREATE+plural, "["+className+"!]"+"!", "", []Argument{
				{Name: protocol.INPUT, Type: "[" + className + protocol.SUFFIX_CREATE_ROW_INPUT + "!]!"},
			}...)
		}
		if operationExists(protocol.UPSERT + plural) {
			my.writeLine("  # ", class.Name, "插入或更新（按on列冲突，缺省主键）")
			onType := className + "ConflictTarget"
			defaultTarget := "PRIMARY"
			if _, ok := class.UpsertConflictTarget("PRIMARY"); !ok {
				onType += "!"
				defaultTarget = ""
			}
			my.writeField(protocol.UPSERT+plural, "["+className+"!]"+"!", "", []Argument{
				{Name: protocol.INPUT, Type: "[" + className + protocol.SUFFIX_UPSERT_ROW_INPUT + "!]!"},
				{Name: protocol.ON, Type: onType, Default: defaultTarget},
			}...)
		}
		batchPlural := mutationBatchName(className)
		if operationExists(protocol.UPDATE + className) {
			if key := my.keyArguments(className, class, true); len(key) > 0 {
				key = append(key, Argument{Name: protocol.INPUT, Type: className + protocol.SUFFIX_UPDATE_INPUT + "!"})
				my.writeField(protocol.UPDATE+className, className, "", key...)
			}
		}
		if operationExists(protocol.UPDATE + batchPlural) {
			my.writeField(protocol.UPDATE+batchPlural, className+protocol.SUFFIX_MUTATION_RESULT+"!", "", []Argument{{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT + "!"}, {Name: protocol.INPUT, Type: className + protocol.SUFFIX_UPDATE_ROW_INPUT + "!"}}...)
		}

		if operationExists(protocol.DELETE + className) {
			my.writeLine("  # ", class.Name, "删除")
			args := append(my.keyArguments(className, class, false), Argument{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT})
			my.writeField(protocol.DELETE+className, protocol.SCALAR_INT+"!", "", args...)
			if key := my.keyArguments(className, class, true); len(key) > 0 && operationExists(protocol.DELETE+className+protocol.SUFFIX_BY_PK) {
				my.writeField(protocol.DELETE+className+protocol.SUFFIX_BY_PK, className, "", key...)
			}
			if operationExists(protocol.DELETE + batchPlural) {
				my.writeField(protocol.DELETE+batchPlural, className+protocol.SUFFIX_MUTATION_RESULT+"!", "", []Argument{{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT + "!"}}...)
			}
		}
		if operationExists("restore"+batchPlural) || operationExists("restore"+className+protocol.SUFFIX_BY_PK) {
			if key := my.keyArguments(className, class, true); len(key) > 0 && operationExists("restore"+className+protocol.SUFFIX_BY_PK) {
				my.writeField("restore"+className+protocol.SUFFIX_BY_PK, className, "", key...)
			}
			if operationExists("restore" + batchPlural) {
				my.writeField("restore"+batchPlural, className+protocol.SUFFIX_MUTATION_RESULT+"!", "", []Argument{{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT + "!"}}...)
			}
		}
		if operationExists(protocol.PURGE+batchPlural) || operationExists(protocol.PURGE+className+protocol.SUFFIX_BY_PK) {
			if key := my.keyArguments(className, class, true); len(key) > 0 && operationExists(protocol.PURGE+className+protocol.SUFFIX_BY_PK) {
				my.writeField(protocol.PURGE+className+protocol.SUFFIX_BY_PK, className, "", key...)
			}
			if operationExists(protocol.PURGE + batchPlural) {
				my.writeField(protocol.PURGE+batchPlural, className+protocol.SUFFIX_MUTATION_RESULT+"!", "", []Argument{{Name: protocol.WHERE, Type: className + protocol.SUFFIX_WHERE_INPUT + "!"}}...)
			}
		}
	})

	my.writeLine("}")
}

func mutationBatchName(className string) string {
	if plural := inflection.Plural(className); plural != className {
		return plural
	}
	return className + protocol.SUFFIX_MANY
}

func (*Renderer) keyArguments(className string, class *protocol.Class, required bool) []Argument {
	primaryKeys := class.PrimaryKeyFields()
	if len(primaryKeys) == 0 {
		return nil
	}
	name, typeName := protocol.ID, protocol.SCALAR_ID
	if len(primaryKeys) > 1 {
		name, typeName = protocol.KEY, className+protocol.SUFFIX_KEY_INPUT
	}
	if required {
		typeName += "!"
	}
	return []Argument{{Name: name, Type: typeName}}
}

// statsSpecs 聚合类型表：一份定义同时驱动聚合结果类型与having过滤类型
// （having镜像聚合结果，各聚合字段复用对应标量的WhereInput操作符，countDistinct恒为Int）
var statsSpecs = []struct {
	comment, stats, having, scalar string
	funcs                          []string
}{
	{"数值", protocol.TYPE_NUMBER_STATS, protocol.TYPE_NUMBER_HAVING, protocol.SCALAR_FLOAT,
		[]string{protocol.FUNCTION_SUM, protocol.FUNCTION_AVG, protocol.FUNCTION_MIN, protocol.FUNCTION_MAX}},
	{"精确数值", "DecimalStats", "DecimalHavingInput", protocol.SCALAR_DECIMAL,
		[]string{protocol.FUNCTION_SUM, protocol.FUNCTION_AVG, protocol.FUNCTION_MIN, protocol.FUNCTION_MAX}},
	{"字符串", protocol.TYPE_STRING_STATS, protocol.TYPE_STRING_HAVING, protocol.SCALAR_STRING,
		[]string{protocol.FUNCTION_MIN, protocol.FUNCTION_MAX}},
	{"日期", protocol.TYPE_DATE_TIME_STATS, protocol.TYPE_DATE_TIME_HAVING, protocol.SCALAR_DATE_TIME,
		[]string{protocol.FUNCTION_MIN, protocol.FUNCTION_MAX}},
}

// statsTypes 标量→(聚合结果类型, having类型)，由statsSpecs推导；Int与Float同归数值聚合
var statsTypes = func() map[string][2]string {
	m := make(map[string][2]string, len(statsSpecs)+1)
	for _, s := range statsSpecs {
		m[s.scalar] = [2]string{s.stats, s.having}
	}
	m[protocol.SCALAR_INT] = m[protocol.SCALAR_FLOAT]
	m[protocol.SCALAR_BIG_INT] = m[protocol.SCALAR_DECIMAL]
	return m
}()

// eachStatColumn 遍历参与统计的真实标量列（跳过虚拟字段/主键），产出其(stats, having)类型对
func (my *Renderer) eachStatColumn(class *protocol.Class, fn func(fieldName string, kinds [2]string)) {
	my.eachField(class, func(fieldName string, field *protocol.Field) {
		if field.Column == "" || field.Virtual || field.IsPrimary {
			return
		}
		if kinds, ok := statsTypes[my.getGraphQLType(field)]; ok {
			fn(fieldName, kinds)
		}
	})
}

// renderStats 渲染统计类型：通用聚合结果 + 每实体的Stats类型（选择驱动编译）
func (my *Renderer) renderStats() {
	intWhere := protocol.SCALAR_INT + protocol.SUFFIX_WHERE_INPUT
	for _, s := range statsSpecs {
		my.writeLine("# ", s.comment, "聚合结果")
		my.writeLine("type ", s.stats, " {")
		for _, fn := range s.funcs {
			my.writeField(fn, s.scalar, "")
		}
		my.writeField(protocol.FUNCTION_COUNT_DISTINCT, protocol.SCALAR_INT, "")
		my.writeLine("}")
		my.writeLine()
	}
	for _, s := range statsSpecs {
		my.writeLine("# ", s.comment, "聚合having过滤")
		my.writeLine("input ", s.having, " {")
		for _, fn := range s.funcs {
			my.writeField(fn, s.scalar+protocol.SUFFIX_WHERE_INPUT, "")
		}
		my.writeField(protocol.FUNCTION_COUNT_DISTINCT, intWhere, "")
		my.writeLine("}")
		my.writeLine()
	}

	// 每实体统计类型 + having入参：key为分组键，count恒有，标量列按类别挂聚合
	my.eachTableClass(func(className string, class *protocol.Class) {
		if !catalogAllows(class, protocol.OP_AGGREGATE) {
			return
		}
		my.writeLine("# ", className, "统计结果")
		my.writeLine("type ", className, protocol.SUFFIX_STATS, " {")
		my.writeField(protocol.FUNCTION_KEY, protocol.SCALAR_JSON, "分组键(无groupBy时为null)")
		my.writeField(protocol.FUNCTION_COUNT, protocol.SCALAR_INT+"!", "")
		my.eachStatColumn(class, func(fieldName string, kinds [2]string) {
			my.writeField(fieldName, kinds[0], "")
		})
		my.writeLine("}")
		my.writeLine()

		// having入参：count + 各可聚合列指向对应聚合having类型
		my.writeLine("# ", className, "having过滤(分组后按聚合值过滤)")
		my.writeLine("input ", className, protocol.SUFFIX_HAVING_INPUT, " {")
		my.writeField(protocol.FUNCTION_COUNT, intWhere, "")
		my.eachStatColumn(class, func(fieldName string, kinds [2]string) {
			my.writeField(fieldName, kinds[1], "")
		})
		my.writeLine("}")
		my.writeLine()
	})
}

// renderPageInfo 游标分页信息类型
func (my *Renderer) renderPageInfo() {
	my.writeLine("# 游标分页信息")
	my.writeLine("type ", protocol.TYPE_PAGE_INFO, " {")
	my.writeField(protocol.HAS_NEXT, protocol.SCALAR_BOOLEAN+"!", "")
	my.writeField(protocol.HAS_PREV, protocol.SCALAR_BOOLEAN+"!", "")
	my.writeField(protocol.START, protocol.SCALAR_CURSOR, "本页第一条的游标")
	my.writeField(protocol.END, protocol.SCALAR_CURSOR, "本页最后一条的游标")
	my.writeLine("}")
	my.writeLine()
}

// renderPaging 渲染分页类型
func (my *Renderer) renderPaging() {
	my.renderPageInfo()
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_CONNECTION, " ", SEPARATOR_LINE, "\n")
	// 为每个实体类生成分页类型
	my.eachTableClass(func(className string, class *protocol.Class) {
		// 生成分页类型
		my.writeLine("# ", className, "分页结果")
		my.writeLine("type ", className, protocol.SUFFIX_RESULT, " {")
		my.writeField(protocol.ITEMS, "["+className+"!]"+"!", "直接返回"+className+"对象数组")
		my.writeField(protocol.TOTAL, protocol.SCALAR_INT+"!", "")
		if len(class.PrimaryKeyFields()) > 0 {
			my.writeField(protocol.PAGE_INFO, protocol.TYPE_PAGE_INFO, "游标分页信息(需配合first/last使用)")
		}
		my.writeLine("}")
		my.writeLine("")
		if catalogAllows(class, protocol.OP_SELECT) && len(class.PrimaryKeyFields()) > 0 {
			my.writeLine("# ", className, "连接分页边")
			my.writeLine("type ", className, protocol.SUFFIX_EDGE, " {")
			my.writeField("node", className+"!", "")
			my.writeField("cursor", protocol.SCALAR_CURSOR+"!", "")
			my.writeLine("}")
			my.writeLine("")
			my.writeLine("# ", className, "连接分页")
			my.writeLine("type ", className, protocol.SUFFIX_CONNECTION, " {")
			my.writeField("edges", "["+className+protocol.SUFFIX_EDGE+"!]"+"!", "")
			my.writeField(protocol.PAGE_INFO, protocol.TYPE_PAGE_INFO+"!", "")
			my.writeField("totalCount", protocol.SCALAR_INT+"!", "")
			my.writeLine("}")
			my.writeLine("")
		}
	})

}

// writeField emits SDL directly; Argument is shared with resolver contracts.
func (my *Renderer) writeField(name, typeName, comment string, args ...Argument) {
	my.write("  ", name)
	if len(args) > 0 {
		my.write("(")
		for i, arg := range args {
			if i > 0 {
				my.write(", ")
			}
			my.write(arg.Name, ": ", arg.Type)
			if arg.Default != "" {
				my.write(" = ", arg.Default)
			}
		}
		my.write(")")
	}
	my.write(": ", typeName)
	if comment != "" {
		my.write("  # ", comment)
	}
	my.write("\n")
}
