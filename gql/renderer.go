package gql

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/gql/renderer"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/utl"
	"github.com/jinzhu/inflection"
	"github.com/rs/zerolog/log"
)

// 分隔线和描述常量
const (
	// 分隔线
	SEPARATOR_LINE = "------------------"

	// 描述性文本
	DESC_SCHEMA_TITLE    = "IdeaBase GraphQL Schema"
	DESC_SCALAR_TYPES    = "自定义标量类型"
	DESC_SORT_ENUM       = "排序方向枚举，包含NULL值处理"
	DESC_IS_ENUM         = "空值条件枚举"

	// 分类标题
	SECTION_PAGING      = "分页相关类型"
	SECTION_FILTER      = "过滤器类型定义"
	SECTION_QUERY       = "查询和变更"
	SECTION_CONNECTION  = "连接和边类型（游标分页）"
)

// 字段描述常量
const (
)

// Renderer 负责将元数据渲染为GraphQL schema
type Renderer struct {
	meta *Metadata
	sb   *strings.Builder
}

// NewRenderer 创建新的Schema渲染器
func NewRenderer(meta *Metadata) *Renderer {
	return &Renderer{
		meta: meta,
		sb:   &strings.Builder{},
	}
}

// Generate 生成完整的GraphQL schema
func (my *Renderer) Generate() (string, error) {
	// 初始化字符串构建器
	my.sb = &strings.Builder{}

	// 添加schema版本和说明
	my.writeLine("# ", DESC_SCHEMA_TITLE)
	my.writeLine("# 版本: ", my.meta.Version, "\n")

	// 定义渲染函数及对应的错误消息
	renderFuncs := []struct {
		name string
		fn   func() error
	}{
		{"标量类型", my.renderScalars},
		{"枚举类型", my.renderEnums},
		{"实体类型", my.renderTypes},
		{"分页类型", my.renderPaging},
		{"统计类型", my.renderStats},
		{"过滤器类型", my.renderFilter},
		{"实体过滤器", my.renderEntity},
		{"排序类型", my.renderSort},
		{"输入类型", my.renderInput},
		{"查询根类型", my.renderQuery},
		{"变更根类型", my.renderMutation},
	}

	// 遍历执行所有渲染函数
	for _, rf := range renderFuncs {
		if err := rf.fn(); err != nil {
			return "", fmt.Errorf("渲染%s失败: %w", rf.name, err)
		}
	}

	// 保存到文件
	content := my.sb.String()
	if err := my.saveToFile(content); err != nil {
		return "", fmt.Errorf("保存schema文件失败: %w", err)
	}

	return content, nil
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

// saveToFile 将生成的Schema保存到文件
func (my *Renderer) saveToFile(content string) error {
	// 写入文件
	filename := filepath.Join(my.meta.cfg.Root, "cfg/schema.graphql")
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return fmt.Errorf("创建schema目录失败: %w", err)
	}
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		return fmt.Errorf("写入schema文件失败: %w", err)
	}

	log.Info().Str("path", filename).Msg("Schema文件已生成")
	return nil
}

// renderScalars 渲染标量类型
func (my *Renderer) renderScalars() error {
	my.writeLine("# ", DESC_SCALAR_TYPES)
	my.writeLine("scalar ", SCALAR_JSON)
	my.writeLine("scalar ", SCALAR_CURSOR)
	my.writeLine("scalar ", SCALAR_DATE_TIME)
	my.writeLine()
	return nil
}

// renderEnums 渲染枚举类型
func (my *Renderer) renderEnums() error {
	// 渲染排序方向枚举
	my.writeLine("# ", DESC_SORT_ENUM)
	my.writeLine("enum ", TYPE_SORT_DIRECTION, " {")
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

	return nil
}

// renderTypes 渲染所有实体类型定义
func (my *Renderer) renderTypes() error {
	// 遍历所有类定义，确保只使用类名作为键
	keys := utl.SortKeys(my.meta.Nodes)
	for _, className := range keys {
		class := my.meta.Nodes[className]
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 判断是否应该跳过中间表类
		if class.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}

		// 添加类型描述
		if class.Description != "" {
			my.writeLine("# ", class.Description)
		}

		// 开始类型定义
		my.writeLine("type ", className, " {")

		// 添加所有字段，确保只处理真正的字段名
		fields := utl.SortKeys(class.Fields)
		for _, fieldName := range fields {
			field := class.Fields[fieldName]
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 判断是否应该跳过中间表字段
			if field.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
				continue
			}

			// 判断字段类型是否引用了中间表类型
			if !my.meta.cfg.Metadata.ShowThrough {
				// 检查字段是否引用了中间表类型
				refType := field.Type
				if field.Relation != nil && field.Relation.TargetClass != "" {
					refType = field.Relation.TargetClass
				}

				// 如果引用的类型是中间表类型，则跳过该字段
				if refClass, exists := my.meta.Nodes[refType]; exists && refClass.IsThrough {
					continue
				}
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

			// 列表关系字段支持嵌套过滤/排序/分页参数
			if field.Column == "" && field.Relation != nil && field.IsList {
				target := field.Relation.TargetClass
				my.writeField(fieldName, typeName, renderer.WithArgs([]renderer.Argument{
					{Name: WHERE, Type: target + SUFFIX_WHERE_INPUT},
					{Name: SORT, Type: "[" + target + SUFFIX_SORT_INPUT + "!]"},
					{Name: LIMIT, Type: SCALAR_INT},
					{Name: OFFSET, Type: SCALAR_INT},
				}...))
				continue
			}

			// 输出字段定义
			my.writeLine("  ", fieldName, ": ", typeName)
		}

		// 结束类型定义
		my.writeLine("}")
		my.writeLine()
	}

	return nil
}

// getGraphQLType 获取GraphQL类型
func (my *Renderer) getGraphQLType(field *protocol.Field) string {
	fieldType := field.Type

	// 处理集合类型
	if field.IsList {
		innerType := fieldType
		if strings.HasPrefix(innerType, "[") && strings.HasSuffix(innerType, "]") {
			innerType = innerType[1 : len(innerType)-1]
		}

		// 检查内部类型是否是类名
		if _, exists := my.meta.Nodes[innerType]; exists {
			// 如果是类名，直接使用类名
			return "[" + innerType + "]"
		}

		// 避免递归调用导致嵌套数组，直接处理内部类型
		innerField := &protocol.Field{
			Type:      innerType,
			IsPrimary: false,
			IsList:    false, // 重要：确保内部字段不是集合类型
		}
		return "[" + my.getGraphQLType(innerField) + "]"
	}

	// 1. 主键固定映射为ID类型
	if field.IsPrimary {
		return SCALAR_ID
	}

	// 处理标量类型
	if fieldType == SCALAR_STRING ||
		fieldType == SCALAR_INT ||
		fieldType == SCALAR_FLOAT ||
		fieldType == SCALAR_BOOLEAN ||
		fieldType == SCALAR_ID ||
		fieldType == SCALAR_JSON ||
		fieldType == SCALAR_CURSOR ||
		fieldType == SCALAR_DATE_TIME {
		return fieldType
	}

	// 2. 只从配置中获取类型映射
	if my.meta != nil && my.meta.cfg != nil && my.meta.cfg.Schema.TypeMapping != nil {
		if gqlType, ok := my.meta.cfg.Schema.TypeMapping[fieldType]; ok {
			return gqlType
		}
	}

	// 3. 检查是否是类名
	if _, exists := my.meta.Nodes[fieldType]; exists {
		// 如果是类名，直接使用类名
		return fieldType
	}

	// 4. 确保返回非空实体类型
	if fieldType == "" {
		// 如果类型为空，使用默认类型
		return SCALAR_STRING
	}

	// 默认假设是实体类型
	return fieldType
}

// writableFields 返回类的可写字段名（排除主键、时间戳、虚拟与中间表字段）
func (my *Renderer) writableFields(class *protocol.Class) []string {
	names := make([]string, 0, len(class.Fields))
	for _, fieldName := range utl.SortKeys(class.Fields) {
		field := class.Fields[fieldName]
		// 跳过列名索引、无列字段（关系/resolver）、自动生成字段（主键/时间戳）、虚拟字段与中间表字段
		if fieldName != field.Name || field.Virtual || field.Column == "" ||
			field.IsPrimary ||
			strings.EqualFold(fieldName, "createdAt") ||
			strings.EqualFold(fieldName, "updatedAt") ||
			(field.IsThrough && !my.meta.cfg.Metadata.ShowThrough) {
			continue
		}
		names = append(names, fieldName)
	}
	return names
}

// writeRelationOps 输入类型中的列表关系操作字段（connect/disconnect原子挂接）
func (my *Renderer) writeRelationOps(class *protocol.Class) {
	for _, fieldName := range utl.SortKeys(class.Fields) {
		field := class.Fields[fieldName]
		// 仅列表关系虚拟字段（一对多/多对多），中间表隐藏时跳过
		if fieldName != field.Name || field.Column != "" || field.Relation == nil || !field.IsList {
			continue
		}
		if field.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}
		if target, ok := my.meta.Nodes[field.Relation.TargetClass]; ok && target.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}
		my.writeField(fieldName, field.Relation.TargetClass+"RelationInput")
	}
}

// renderInput 渲染输入类型
func (my *Renderer) renderInput() error {
	// 为每个实体类生成创建和更新输入类型
	keys := utl.SortKeys(my.meta.Nodes)
	for _, className := range keys {
		class := my.meta.Nodes[className]
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 判断是否应该跳过中间表类
		if class.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}

		// 无可写字段的类（如纯主键表）不生成输入类型
		writable := my.writableFields(class)
		if len(writable) == 0 {
			continue
		}

		// 生成创建输入类型
		my.writeLine("# ", className, "创建输入")
		my.writeLine("input ", className, SUFFIX_CREATE_INPUT, " {")
		for _, fieldName := range writable {
			field := class.Fields[fieldName]
			typeName := my.getGraphQLType(field)
			// 非空字段添加!；外键列放宽为可选（嵌套创建时由父行锚点填充，数据库NOT NULL兜底）
			if !field.Nullable && field.Relation == nil {
				typeName += "!"
			}
			my.writeField(fieldName, typeName)
		}
		my.writeRelationOps(class)
		my.writeLine("}")
		my.writeLine("")

		// 生成更新输入类型（全部可选）
		my.writeLine("# ", className, "更新输入")
		my.writeLine("input ", className, SUFFIX_UPDATE_INPUT, " {")
		for _, fieldName := range writable {
			my.writeField(fieldName, my.getGraphQLType(class.Fields[fieldName]))
		}
		my.writeRelationOps(class)

		my.writeLine("}")
		my.writeLine("")
	}

	// 按目标类生成关系操作输入：connect/disconnect挂接解除既有行，create内联建新行
	for _, className := range keys {
		class := my.meta.Nodes[className]
		if className != class.Name || (class.IsThrough && !my.meta.cfg.Metadata.ShowThrough) {
			continue
		}
		my.writeLine("# ", className, "关系操作（原子挂接/解除/内联创建）")
		my.writeLine("input ", className, "RelationInput {")
		my.writeField("connect", SCALAR_ID, renderer.ListNonNull(), renderer.WithComment("挂接既有行主键"))
		my.writeField("disconnect", SCALAR_ID, renderer.ListNonNull(), renderer.WithComment("解除既有行主键(仅更新)"))
		if len(my.writableFields(class)) > 0 {
			my.writeField("create", "["+className+SUFFIX_CREATE_INPUT+"!]", renderer.WithComment("内联创建新行"))
		}
		my.writeLine("}")
		my.writeLine()
	}

	return nil
}

// renderFilter 渲染基础过滤器类型
func (my *Renderer) renderFilter() error {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_FILTER, " ", SEPARATOR_LINE, "\n")

	// 定义过滤器映射表，每种类型支持的操作
	keys := utl.SortKeys(grouping)
	for _, scalarType := range keys {
		operators := grouping[scalarType]
		filterName := scalarType + SUFFIX_WHERE_INPUT
		my.writeLine("# ", scalarType, "过滤器")
		my.writeLine("input ", filterName, " {")

		// 使用map防止操作符重复
		renderedOps := make(map[string]bool)

		// 渲染该类型支持的所有操作符
		for _, op := range operators {
			// 跳过已经渲染过的操作符
			if renderedOps[op.Name] {
				continue
			}
			renderedOps[op.Name] = true

			if op.Name == HAS_KEY || op.Name == HAS_KEY_ANY || op.Name == HAS_KEY_ALL {
				my.writeField(op.Name, SCALAR_STRING, renderer.WithComment(op.Description))
			} else if op.Name == IN || op.Name == NI {
				my.writeField(op.Name, scalarType, renderer.ListNonNull(), renderer.WithComment(op.Description))
			} else if op.Name == IS {
				my.writeField(op.Name, ENUM_IS_INPUT, renderer.WithComment(op.Description))
			} else {
				my.writeField(op.Name, scalarType, renderer.WithComment(op.Description))
			}
		}

		my.writeLine("}")
		my.writeLine()
	}
	return nil
}

// renderEntity 渲染实体过滤器
func (my *Renderer) renderEntity() error {
	// 为每个实体类生成过滤器
	keys := utl.SortKeys(my.meta.Nodes)
	for _, className := range keys {
		class := my.meta.Nodes[className]
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 判断是否应该跳过中间表类
		if class.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}

		// 生成过滤器类型
		my.writeLine("# ", className, "查询条件")
		my.writeLine("input ", className, SUFFIX_WHERE_INPUT, " {")

		// 添加常规字段过滤条件
		fields := utl.SortKeys(class.Fields)
		for _, fieldName := range fields {
			field := class.Fields[fieldName]
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 判断是否应该跳过中间表字段
			if field.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
				continue
			}

			// 跳过虚拟关系字段
			if field.Virtual {
				continue
			}

			// 判断字段类型是否引用了中间表类型
			if !my.meta.cfg.Metadata.ShowThrough {
				// 检查字段是否引用了中间表类型
				refType := field.Type
				if field.Relation != nil && field.Relation.TargetClass != "" {
					refType = field.Relation.TargetClass
				}

				// 如果引用的类型是中间表类型，则跳过该字段
				if refClass, exists := my.meta.Nodes[refType]; exists && refClass.IsThrough {
					continue
				}
			}

			// 获取字段类型
			fieldType := my.getGraphQLType(field)
			my.writeLine("  ", fieldName, ": ", fieldType, SUFFIX_WHERE_INPUT)
		}

		// 添加布尔逻辑操作符
		my.writeLine("  and: [", className, SUFFIX_WHERE_INPUT, "!]")
		my.writeLine("  or: [", className, SUFFIX_WHERE_INPUT, "!]")
		my.writeLine("  not: ", className, SUFFIX_WHERE_INPUT)

		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderSort 渲染排序类型
func (my *Renderer) renderSort() error {
	// 为每个实体类生成排序类型
	keys := utl.SortKeys(my.meta.Nodes)
	for _, className := range keys {
		class := my.meta.Nodes[className]
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 判断是否应该跳过中间表类
		if class.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}

		// 生成排序类型
		my.writeLine("# ", className, "排序")
		my.writeLine("input ", className, SUFFIX_SORT_INPUT, " {")

		// 添加可排序字段
		fields := utl.SortKeys(class.Fields)
		for _, fieldName := range fields {
			field := class.Fields[fieldName]
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 判断是否应该跳过中间表字段
			if field.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
				continue
			}

			// 判断字段类型是否引用了中间表类型
			if !my.meta.cfg.Metadata.ShowThrough {
				// 检查字段是否引用了中间表类型
				refType := field.Type
				if field.Relation != nil && field.Relation.TargetClass != "" {
					refType = field.Relation.TargetClass
				}

				// 如果引用的类型是中间表类型，则跳过该字段
				if refClass, exists := my.meta.Nodes[refType]; exists && refClass.IsThrough {
					continue
				}
			}

			// 添加排序选项
			my.writeField(fieldName, TYPE_SORT_DIRECTION)
		}

		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderQuery 渲染查询根类型与订阅根类型（订阅镜像实体查询字段）
func (my *Renderer) renderQuery() error {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_QUERY, " ", SEPARATOR_LINE, "\n")

	// 实体查询字段渲染闭包，Query与Subscription共用
	writeEntityField := func(className string) {
		args := []renderer.Argument{}
		// 声明了搜索列的实体提供全文搜索参数
		if class := my.meta.Nodes[className]; class != nil && len(class.Search) > 0 {
			args = append(args, renderer.Argument{Name: SEARCH, Type: SCALAR_STRING})
		}
		my.writeLine("  # ", className, "查询")
		my.writeField(
			strcase.ToLowerCamel(inflection.Plural(className)),
			className+SUFFIX_RESULT,
			renderer.NonNull(),
			renderer.WithMultilineArgs(),
			renderer.WithArgs(append(args, []renderer.Argument{
				{Name: ID, Type: SCALAR_ID},
				{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
				{Name: SORT, Type: "[" + className + SUFFIX_SORT_INPUT + "!]"},
				{Name: DISTINCT, Type: "[" + SCALAR_STRING + "!]"},
				{Name: LIMIT, Type: SCALAR_INT},
				{Name: OFFSET, Type: SCALAR_INT},
				{Name: FIRST, Type: SCALAR_INT},
				{Name: AFTER, Type: SCALAR_CURSOR},
				{Name: LAST, Type: SCALAR_INT},
				{Name: BEFORE, Type: SCALAR_CURSOR},
			}...)...),
		)
	}

	// 收集可渲染的实体类名
	var names []string
	for _, className := range utl.SortKeys(my.meta.Nodes) {
		class := my.meta.Nodes[className]
		// 跳过表名索引与隐藏的中间表类
		if className != class.Name || (class.IsThrough && !my.meta.cfg.Metadata.ShowThrough) {
			continue
		}
		names = append(names, className)
	}

	my.writeLine("# 查询根类型")
	my.writeLine("type Query {")
	for _, className := range names {
		writeEntityField(className)

		// 统计查询
		my.writeLine("  # ", className, "统计")
		my.writeField(
			strcase.ToLowerCamel(className)+SUFFIX_STATS,
			"["+className+SUFFIX_STATS+"!]",
			renderer.NonNull(),
			renderer.WithArgs([]renderer.Argument{
				{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
				{Name: GROUP_BY, Type: "[" + SCALAR_STRING + "!]"},
				{Name: LIMIT, Type: SCALAR_INT},
				{Name: OFFSET, Type: SCALAR_INT},
			}...),
		)
	}
	my.writeLine("}")
	my.writeLine()

	// 订阅根类型：轮询式订阅，能力与实体查询一致
	my.writeLine("# 订阅根类型")
	my.writeLine("type Subscription {")
	for _, className := range names {
		writeEntityField(className)
	}
	my.writeLine("}")
	my.writeLine()
	return nil
}

// renderMutation 渲染变更根类型
func (my *Renderer) renderMutation() error {
	my.writeLine("# 突变根类型")
	my.writeLine("type Mutation {")

	// 按排序顺序渲染每种类型的变更操作
	keys := utl.SortKeys(my.meta.Nodes)
	for _, className := range keys {
		class := my.meta.Nodes[className]
		// 跳过表名别名
		if className != class.Name {
			continue
		}
		// 跳过中间表类（除非配置了显示中间表）
		if class.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}

		// 无可写字段的类（如纯主键表）不生成创建/更新操作
		if len(my.writableFields(class)) > 0 {
			plural := inflection.Plural(className)

			my.writeLine("  # ", class.Name, "创建")
			my.writeField(CREATE+className, className, renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
				{Name: INPUT, Type: className + SUFFIX_CREATE_INPUT + "!"},
			}...))

			my.writeLine("  # ", class.Name, "批量创建")
			my.writeField(CREATE+plural, "["+className+"!]", renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
				{Name: INPUT, Type: "[" + className + SUFFIX_CREATE_INPUT + "!]!"},
			}...))

			my.writeLine("  # ", class.Name, "插入或更新（按on列冲突，缺省主键）")
			my.writeField(UPSERT+plural, "["+className+"!]", renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
				{Name: INPUT, Type: "[" + className + SUFFIX_CREATE_INPUT + "!]!"},
				{Name: "on", Type: "[" + SCALAR_STRING + "!]"},
			}...))

			my.writeLine("  # ", class.Name, "更新")
			my.writeField(UPDATE+className, className, renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
				{Name: INPUT, Type: className + SUFFIX_UPDATE_INPUT + "!"},
				{Name: ID, Type: SCALAR_ID},
				{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
			}...))
		}

		my.writeLine("  # ", class.Name, "删除")
		my.writeField(DELETE+className, SCALAR_INT, renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
			{Name: ID, Type: SCALAR_ID},
			{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
		}...))
	}

	my.writeLine("}")
	return nil
}

// statsKind 标量字段对应的统计类型，空串表示不参与统计
func statsKind(typeName string) string {
	switch typeName {
	case SCALAR_INT, SCALAR_FLOAT:
		return TYPE_NUMBER_STATS
	case SCALAR_STRING:
		return TYPE_STRING_STATS
	case SCALAR_DATE_TIME:
		return TYPE_DATE_TIME_STATS
	}
	return ""
}

// renderStats 渲染统计类型：通用聚合结果 + 每实体的Stats类型（选择驱动编译）
func (my *Renderer) renderStats() error {
	my.writeLine("# 数值聚合结果")
	my.writeLine("type ", TYPE_NUMBER_STATS, " {")
	my.writeField(FUNCTION_SUM, SCALAR_FLOAT)
	my.writeField(FUNCTION_AVG, SCALAR_FLOAT)
	my.writeField(FUNCTION_MIN, SCALAR_FLOAT)
	my.writeField(FUNCTION_MAX, SCALAR_FLOAT)
	my.writeField(FUNCTION_COUNT_DISTINCT, SCALAR_INT)
	my.writeLine("}")
	my.writeLine()

	my.writeLine("# 字符串聚合结果")
	my.writeLine("type ", TYPE_STRING_STATS, " {")
	my.writeField(FUNCTION_MIN, SCALAR_STRING)
	my.writeField(FUNCTION_MAX, SCALAR_STRING)
	my.writeField(FUNCTION_COUNT_DISTINCT, SCALAR_INT)
	my.writeLine("}")
	my.writeLine()

	my.writeLine("# 日期聚合结果")
	my.writeLine("type ", TYPE_DATE_TIME_STATS, " {")
	my.writeField(FUNCTION_MIN, SCALAR_DATE_TIME)
	my.writeField(FUNCTION_MAX, SCALAR_DATE_TIME)
	my.writeField(FUNCTION_COUNT_DISTINCT, SCALAR_INT)
	my.writeLine("}")
	my.writeLine()

	// 每实体统计类型：key为分组键，count恒有，标量列按类别挂聚合
	for _, className := range utl.SortKeys(my.meta.Nodes) {
		class := my.meta.Nodes[className]
		if className != class.Name || (class.IsThrough && !my.meta.cfg.Metadata.ShowThrough) {
			continue
		}

		my.writeLine("# ", className, "统计结果")
		my.writeLine("type ", className, SUFFIX_STATS, " {")
		my.writeField(FUNCTION_KEY, SCALAR_JSON, renderer.WithComment("分组键(无groupBy时为null)"))
		my.writeField(FUNCTION_COUNT, SCALAR_INT, renderer.NonNull())
		for _, fieldName := range utl.SortKeys(class.Fields) {
			field := class.Fields[fieldName]
			// 仅真实标量列参与统计（跳过列名索引/虚拟字段/主键）
			if fieldName != field.Name || field.Column == "" || field.Virtual || field.IsPrimary {
				continue
			}
			if kind := statsKind(my.getGraphQLType(field)); kind != "" {
				my.writeField(fieldName, kind)
			}
		}
		my.writeLine("}")
		my.writeLine()
	}
	return nil
}

// renderPageInfo 游标分页信息类型
func (my *Renderer) renderPageInfo() {
	my.writeLine("# 游标分页信息")
	my.writeLine("type ", TYPE_PAGE_INFO, " {")
	my.writeField("hasNext", SCALAR_BOOLEAN, renderer.NonNull())
	my.writeField("hasPrev", SCALAR_BOOLEAN, renderer.NonNull())
	my.writeField("start", SCALAR_CURSOR, renderer.WithComment("本页第一条的游标"))
	my.writeField("end", SCALAR_CURSOR, renderer.WithComment("本页最后一条的游标"))
	my.writeLine("}")
	my.writeLine()
}

// renderPaging 渲染分页类型
func (my *Renderer) renderPaging() error {
	my.renderPageInfo()
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_CONNECTION, " ", SEPARATOR_LINE, "\n")
	keys := utl.SortKeys(my.meta.Nodes)
	// 为每个实体类生成分页类型
	for _, className := range keys {
		class := my.meta.Nodes[className]
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 判断是否应该跳过中间表类
		if class.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
			continue
		}

		// 生成分页类型
		my.writeLine("# ", className, "分页结果")
		my.writeLine("type ", className, SUFFIX_RESULT, " {")
		my.writeField(ITEMS, className, renderer.NonNull(), renderer.ListNonNull(), renderer.WithComment("直接返回"+className+"对象数组"))
		my.writeField(TOTAL, SCALAR_INT, renderer.NonNull())
		my.writeField(PAGE_INFO, TYPE_PAGE_INFO, renderer.WithComment("游标分页信息(需配合first/last使用)"))
		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// writeField 使用优化的子包渲染字段
func (my *Renderer) writeField(name string, typeName string, options ...renderer.Option) {
	// 使用子包中的便捷方法生成字段字符串
	fieldStr := renderer.MakeField(name, typeName, options...)
	my.writeLine(fieldStr)
}
