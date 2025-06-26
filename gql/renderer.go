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
	DESC_PAGE_INFO       = "页面信息（用于游标分页）"
	DESC_GROUP_BY        = "聚合分组选项"
	DESC_RELATION        = "关联操作"
	DESC_RELATION_OP     = "关系操作"
	DESC_NUMBER_STATS    = "数值聚合结果"
	DESC_STRING_STATS    = "字符串聚合结果"
	DESC_DATE_TIME_STATS = "日期聚合结果"

	// 分类标题
	SECTION_PAGING      = "分页相关类型"
	SECTION_FILTER      = "过滤器类型定义"
	SECTION_QUERY       = "查询和变更"
	SECTION_AGGREGATION = "聚合函数相关类型"
	SECTION_CONNECTION  = "连接和边类型（游标分页）"
)

// 字段描述常量
const (
	COMMENT_GROUP_KEY    = "分组键"
	COMMENT_COUNT        = "计数"
	COMMENT_HAS_NEXT     = "是否有下一页"
	COMMENT_HAS_PREV     = "是否有上一页"
	COMMENT_START_CURSOR = "当前页第一条记录的游标"
	COMMENT_END_CURSOR   = "当前页最后一条记录的游标"
	COMMENT_GROUP_FIELDS = "分组字段"
	COMMENT_HAVING       = "分组过滤条件"
	COMMENT_LIMIT        = "分组结果限制"
	COMMENT_SORT         = "分组结果排序"
	COMMENT_SUM          = "总和"
	COMMENT_AVG          = "平均值"
	COMMENT_MIN          = "最小值"
	COMMENT_MAX          = "最大值"
	COMMENT_DISTINCT     = "去重计数"
	COMMENT_MIN_STRING   = "最小值(按字典序)"
	COMMENT_MAX_STRING   = "最大值(按字典序)"
	COMMENT_MIN_DATE     = "最早时间"
	COMMENT_MAX_DATE     = "最晚时间"
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
		{"通用类型", my.renderCommon},
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

// renderCommon 渲染通用类型
func (my *Renderer) renderCommon() error {
	// 渲染分页信息类型
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_PAGING, " ", SEPARATOR_LINE, "\n")
	my.writeLine("# ", DESC_PAGE_INFO)
	my.writeLine("type ", TYPE_PAGE_INFO, " {")
	my.writeField("hasNext", SCALAR_BOOLEAN, renderer.NonNull(), renderer.WithComment(COMMENT_HAS_NEXT))
	my.writeField("hasPrev", SCALAR_BOOLEAN, renderer.NonNull(), renderer.WithComment(COMMENT_HAS_PREV))
	my.writeField("start", SCALAR_CURSOR, renderer.WithComment(COMMENT_START_CURSOR))
	my.writeField("end", SCALAR_CURSOR, renderer.WithComment(COMMENT_END_CURSOR))
	my.writeLine("}")
	my.writeLine()

	// 渲染分组选项类型
	my.writeLine("# ", DESC_GROUP_BY)
	my.writeLine("input ", TYPE_GROUP_BY, " {")
	my.writeField("fields", SCALAR_STRING, renderer.ListNonNull(), renderer.WithComment(COMMENT_GROUP_FIELDS))
	my.writeField("having", SCALAR_JSON, renderer.WithComment(COMMENT_HAVING))
	my.writeField("limit", SCALAR_INT, renderer.WithComment(COMMENT_LIMIT))
	my.writeField("sort", SCALAR_JSON, renderer.WithComment(COMMENT_SORT))
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

		// 生成创建输入类型
		my.writeLine("# ", className, "创建输入")
		my.writeLine("input ", className, SUFFIX_CREATE_INPUT, " {")
		// 添加创建时的必要字段
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

			// 跳过虚拟字段，这些通常是关系或计算字段
			if field.Virtual {
				continue
			}

			typeName := my.getGraphQLType(field)
			// 非空字段添加!
			if !field.Nullable {
				typeName += "!"
			}

			my.writeField(fieldName, typeName)
		}
		my.writeLine("}")
		my.writeLine("")

		// 生成更新输入类型
		my.writeLine("# ", className, "更新输入")
		my.writeLine("input ", className, SUFFIX_UPDATE_INPUT, " {")
		// 添加可更新字段，全部为可选
		for _, fieldName := range fields {
			field := class.Fields[fieldName]
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 排除自动生成和只读字段
			if strings.EqualFold(fieldName, "id") ||
				strings.EqualFold(fieldName, "createdAt") ||
				strings.EqualFold(fieldName, "updatedAt") {
				continue
			}

			// 判断是否应该跳过中间表字段
			if field.IsThrough && !my.meta.cfg.Metadata.ShowThrough {
				continue
			}

			// 跳过虚拟字段，这些通常是关系或计算字段
			if field.Virtual {
				continue
			}

			typeName := my.getGraphQLType(field)
			my.writeField(fieldName, typeName)
		}

		// 添加关系操作字段
		my.writeLine("  # 关系操作")
		my.writeLine("  relation: RelationInput")

		my.writeLine("}")
		my.writeLine("")
	}

	// 渲染关系操作输入类型
	my.writeLine("# ", DESC_RELATION)
	my.writeLine("input RelationInput {")
	my.writeField("id", SCALAR_ID, renderer.NonNull())
	my.writeField("connect", SCALAR_ID, renderer.ListNonNull())
	my.writeField("disconnect", SCALAR_ID, renderer.ListNonNull())
	my.writeLine("}")
	my.writeLine("")

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

// renderQuery 渲染查询根类型
func (my *Renderer) renderQuery() error {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_QUERY, " ", SEPARATOR_LINE, "\n")
	my.writeLine("# 查询根类型")
	my.writeLine("type Query {")

	// 为每个实体类生成查询字段
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

		// 统一查询（支持单条和多条）
		my.writeLine("  # ", className, "查询")
		my.writeField(
			strcase.ToLowerCamel(inflection.Plural(className)),
			className+SUFFIX_PAGE,
			renderer.NonNull(),
			renderer.WithMultilineArgs(),
			renderer.WithArgs([]renderer.Argument{
				{Name: ID, Type: SCALAR_ID},
				{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
				{Name: SORT, Type: "[" + className + SUFFIX_SORT_INPUT + "!]"},
				{Name: LIMIT, Type: SCALAR_INT},
				{Name: OFFSET, Type: SCALAR_INT},
				{Name: FIRST, Type: SCALAR_INT},
				{Name: LAST, Type: SCALAR_INT},
				{Name: AFTER, Type: SCALAR_CURSOR},
				{Name: BEFORE, Type: SCALAR_CURSOR},
			}...),
		)

		// 统计查询
		my.writeLine("  # ", className, "统计查询")
		my.writeField(
			strcase.ToLowerCamel(className)+SUFFIX_STATS,
			className+SUFFIX_STATS,
			renderer.NonNull(),
			renderer.WithArgs([]renderer.Argument{
				{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
				{Name: GROUP_BY, Type: TYPE_GROUP_BY},
			}...),
		)
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

		my.writeLine("  # ", class.Name, "创建")
		my.writeField(CREATE+className, className, renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
			{Name: INPUT, Type: className + SUFFIX_CREATE_INPUT + "!"},
		}...))

		my.writeLine("  # ", class.Name, "更新")
		my.writeField(UPDATE+className, className, renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
			{Name: INPUT, Type: className + SUFFIX_UPDATE_INPUT + "!"},
			{Name: ID, Type: SCALAR_ID},
			{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
		}...))

		my.writeLine("  # ", class.Name, "删除")
		my.writeField(DELETE+className, SCALAR_INT, renderer.NonNull(), renderer.WithArgs([]renderer.Argument{
			{Name: ID, Type: SCALAR_ID},
			{Name: WHERE, Type: className + SUFFIX_WHERE_INPUT},
		}...))
	}

	my.writeLine("}")
	return nil
}

// renderStats 渲染统计类型
func (my *Renderer) renderStats() error {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_AGGREGATION, " ", SEPARATOR_LINE, "\n")

	// 数值聚合结果
	my.writeLine("# ", DESC_NUMBER_STATS)
	my.writeLine("type ", TYPE_NUMBER_STATS, " {")
	my.writeField(FUNCTION_SUM, SCALAR_FLOAT, renderer.WithComment(COMMENT_SUM))
	my.writeField(FUNCTION_AVG, SCALAR_FLOAT, renderer.WithComment(COMMENT_AVG))
	my.writeField(FUNCTION_MIN, SCALAR_FLOAT, renderer.WithComment(COMMENT_MIN))
	my.writeField(FUNCTION_MAX, SCALAR_FLOAT, renderer.WithComment(COMMENT_MAX))
	my.writeField(FUNCTION_COUNT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_COUNT))
	my.writeField(FUNCTION_COUNT_DISTINCT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_DISTINCT))
	my.writeLine("}")
	my.writeLine()

	// 日期聚合结果
	my.writeLine("# ", DESC_DATE_TIME_STATS)
	my.writeLine("type ", TYPE_DATE_TIME_STATS, " {")
	my.writeField(FUNCTION_MIN, SCALAR_DATE_TIME, renderer.WithComment(COMMENT_MIN_DATE))
	my.writeField(FUNCTION_MAX, SCALAR_DATE_TIME, renderer.WithComment(COMMENT_MAX_DATE))
	my.writeField(FUNCTION_COUNT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_COUNT))
	my.writeField(FUNCTION_COUNT_DISTINCT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_DISTINCT))
	my.writeLine("}")
	my.writeLine()

	// 字符串聚合结果
	my.writeLine("# ", DESC_STRING_STATS)
	my.writeLine("type ", TYPE_STRING_STATS, " {")
	my.writeField(FUNCTION_MIN, SCALAR_STRING, renderer.WithComment(COMMENT_MIN_STRING))
	my.writeField(FUNCTION_MAX, SCALAR_STRING, renderer.WithComment(COMMENT_MAX_STRING))
	my.writeField(FUNCTION_COUNT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_COUNT))
	my.writeField(FUNCTION_COUNT_DISTINCT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_DISTINCT))
	my.writeLine("}")
	my.writeLine()
	keys := utl.SortKeys(my.meta.Nodes)
	// 为每个实体类生成统计类型
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

		// 生成统计类型
		my.writeLine("# ", className, "聚合")
		my.writeLine("type ", className, SUFFIX_STATS, " {")
		my.writeField(FUNCTION_COUNT, SCALAR_INT, renderer.NonNull())

		// 添加统计字段
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

			// 根据字段类型添加对应的统计类型
			typeName := my.getGraphQLType(field)
			switch typeName {
			case SCALAR_ID, SCALAR_INT, SCALAR_FLOAT:
				my.writeField(fieldName, TYPE_NUMBER_STATS)
			case SCALAR_STRING:
				my.writeField(fieldName, TYPE_STRING_STATS)
			case SCALAR_DATE_TIME:
				my.writeField(fieldName, TYPE_DATE_TIME_STATS)
			default:
				// 跳过不支持统计的类型
				continue
			}
		}

		// 添加分组聚合
		my.writeLine("  # 分组聚合")
		my.writeField(GROUP_BY, "["+className+SUFFIX_GROUP+"!]")
		my.writeLine("}")
		my.writeLine("")

		// 生成对应的分组类型
		my.writeLine("# ", className, "分组结果")
		my.writeLine("type ", className, SUFFIX_GROUP, " {")
		my.writeField(FUNCTION_KEY, SCALAR_JSON, renderer.NonNull(), renderer.WithComment(COMMENT_GROUP_KEY))
		my.writeField(FUNCTION_COUNT, SCALAR_INT, renderer.NonNull(), renderer.WithComment(COMMENT_COUNT))
		my.writeLine("  # 可以包含其他聚合字段")
		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderPaging 渲染分页类型
func (my *Renderer) renderPaging() error {
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
		my.writeLine("type ", className, SUFFIX_PAGE, " {")
		my.writeField(ITEMS, className, renderer.NonNull(), renderer.ListNonNull(), renderer.WithComment("直接返回"+className+"对象数组"))
		my.writeField(PAGE_INFO, TYPE_PAGE_INFO, renderer.NonNull())
		my.writeField(TOTAL, SCALAR_INT, renderer.NonNull())
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
