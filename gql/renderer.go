package gql

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/renderer/field"
	"github.com/jinzhu/inflection"
	"github.com/rs/zerolog/log"
)

// 类型常量
const (
	// GraphQL类型名称
	TYPE_SORT_DIRECTION  = "SortDirection"
	TYPE_PAGE_INFO       = "PageInfo"
	TYPE_GROUP_BY        = "GroupBy"
	TYPE_NUMBER_STATS    = "NumberStats"
	TYPE_STRING_STATS    = "StringStats"
	TYPE_DATE_TIME_STATS = "DateTimeStats"

	// 类型名称后缀
	SUFFIX_FILTER       = "Filter"
	SUFFIX_SORT         = "Sort"
	SUFFIX_STATS        = "Stats"
	SUFFIX_GROUP        = "Group"
	SUFFIX_PAGE         = "Page"
	SUFFIX_CREATE_INPUT = "CreateInput"
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
	my.writeField("hasNext", SCALAR_BOOLEAN, field.NonNull(), field.WithComment(COMMENT_HAS_NEXT))
	my.writeField("hasPrev", SCALAR_BOOLEAN, field.NonNull(), field.WithComment(COMMENT_HAS_PREV))
	my.writeField("start", SCALAR_CURSOR, field.WithComment(COMMENT_START_CURSOR))
	my.writeField("end", SCALAR_CURSOR, field.WithComment(COMMENT_END_CURSOR))
	my.writeLine("}")
	my.writeLine()

	// 渲染分组选项类型
	my.writeLine("# ", DESC_GROUP_BY)
	my.writeLine("input ", TYPE_GROUP_BY, " {")
	my.writeField("fields", SCALAR_STRING, field.ListNonNull(), field.WithComment(COMMENT_GROUP_FIELDS))
	my.writeField("having", SCALAR_JSON, field.WithComment(COMMENT_HAVING))
	my.writeField("limit", SCALAR_INT, field.WithComment(COMMENT_LIMIT))
	my.writeField("sort", SCALAR_JSON, field.WithComment(COMMENT_SORT))
	my.writeLine("}")
	my.writeLine()

	return nil
}

// renderTypes 渲染所有实体类型定义
func (my *Renderer) renderTypes() error {
	// 遍历所有类定义，确保只使用类名作为键
	classNames := make([]string, 0, len(my.meta.Nodes))
	for k := range my.meta.Nodes {
		classNames = append(classNames, k)
	}
	sort.Strings(classNames)
	for _, className := range classNames {
		class := my.meta.Nodes[className]
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 添加类型描述
		if class.Description != "" {
			my.writeLine("# ", class.Description)
		}

		// 开始类型定义
		my.writeLine("type ", className, " {")

		// 添加所有字段，确保只处理真正的字段名
		fieldNames := make([]string, 0, len(class.Fields))
		for k := range class.Fields {
			fieldNames = append(fieldNames, k)
		}
		sort.Strings(fieldNames)
		for _, fieldName := range fieldNames {
			field := class.Fields[fieldName]
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
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
func (my *Renderer) getGraphQLType(field *internal.Field) string {
	fieldType := field.Type

	// 处理集合类型
	if field.IsCollection {
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
		innerField := &internal.Field{
			Type:         innerType,
			IsPrimary:    false,
			IsCollection: false, // 重要：确保内部字段不是集合类型
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
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成创建输入类型
		my.writeLine("# ", className, "创建输入")
		my.writeLine("input ", className, SUFFIX_CREATE_INPUT, " {")
		// 添加创建时的必要字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
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
		for fieldName, field := range class.Fields {
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

			typeName := my.getGraphQLType(field)
			// 更新时所有字段都是可选的
			my.writeField(fieldName, typeName)
		}
		my.writeLine("}")
		my.writeLine("")
	}

	// 添加关系输入类型
	my.writeLine("# ", DESC_RELATION)
	my.writeLine("input ConnectInput {")
	my.writeField("id", SCALAR_ID, field.NonNull())
	my.writeLine("}")
	my.writeLine("")

	my.writeLine("# ", DESC_RELATION_OP)
	my.writeLine("input RelationInput {")
	my.writeField("connect", SCALAR_ID, field.ListNonNull())
	my.writeField("disconnect", SCALAR_ID, field.ListNonNull())
	my.writeLine("}")
	my.writeLine("")

	return nil
}

// renderFilter 渲染基础过滤器类型
func (my *Renderer) renderFilter() error {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_FILTER, " ", SEPARATOR_LINE, "\n")

	// 定义过滤器映射表，每种类型支持的操作
	for scalarType, operators := range symbols {
		filterName := scalarType + SUFFIX_FILTER
		my.writeLine("# ", scalarType, "过滤器")
		my.writeLine("input ", filterName, " {")

		// 渲染该类型支持的所有操作符
		for _, op := range operators {
			if op.Name == HAS_KEY || op.Name == HAS_KEY_ANY || op.Name == HAS_KEY_ALL {
				my.writeField(op.Name, SCALAR_STRING, field.WithComment(op.Desc))
			} else if op.Name == IN || op.Name == NI {
				my.writeField(op.Name, scalarType, field.ListNonNull(), field.WithComment(op.Desc))
			} else if op.Name == IS {
				my.writeField(op.Name, "IsInput", field.WithComment(op.Desc))
			} else {
				my.writeField(op.Name, scalarType, field.WithComment(op.Desc))
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
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成过滤器类型
		my.writeLine("# ", className, "查询条件")
		my.writeLine("input ", className, SUFFIX_FILTER, " {")

		// 添加字段过滤条件
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 获取字段类型
			fieldType := my.getGraphQLType(field)
			my.writeField(fieldName, fieldType+"Filter")
		}

		// 添加布尔逻辑操作符
		my.writeField(AND, className+SUFFIX_FILTER, field.ListNonNull())
		my.writeField(OR, className+SUFFIX_FILTER, field.ListNonNull())
		my.writeField(NOT, className+SUFFIX_FILTER)

		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderSort 渲染排序类型
func (my *Renderer) renderSort() error {
	// 为每个实体类生成排序类型
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成排序类型
		my.writeLine("# ", className, "排序")
		my.writeLine("input ", className, SUFFIX_SORT, " {")

		// 添加可排序字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 判断字段是否可排序
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
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 单个实体查询
		my.writeLine("  # 单个", className, "查询")
		my.writeField(strcase.ToLowerCamel(className), className, field.WithArgs([]field.Argument{
			{Name: "id", Type: SCALAR_ID + "!"},
		}...))

		// 统一列表查询（支持两种分页方式）
		my.writeLine("\n  # ", className, "列表查询")
		my.writeField(
			strcase.ToLowerCamel(inflection.Plural(className)),
			className+SUFFIX_PAGE,
			field.NonNull(),
			field.WithMultilineArgs(),
			field.WithArgs([]field.Argument{
				{Name: "filter", Type: className + SUFFIX_FILTER},
				{Name: "sort", Type: "[" + className + SUFFIX_SORT + "!]"},
				{Name: "limit", Type: SCALAR_INT},
				{Name: "offset", Type: SCALAR_INT},
				{Name: "first", Type: SCALAR_INT},
				{Name: "last", Type: SCALAR_INT},
				{Name: "after", Type: SCALAR_CURSOR},
				{Name: "before", Type: SCALAR_CURSOR},
			}...),
		)

		// 统计查询
		my.writeLine("\n  # ", className, "统计查询")
		my.writeField(
			strcase.ToLowerCamel(className)+SUFFIX_STATS,
			className+SUFFIX_STATS,
			field.NonNull(),
			field.WithArgs([]field.Argument{
				{Name: "filter", Type: className + SUFFIX_FILTER},
				{Name: "groupBy", Type: TYPE_GROUP_BY},
			}...),
		)
	}

	my.writeLine("}")
	my.writeLine()
	return nil
}

// renderMutation 渲染变更根类型
func (my *Renderer) renderMutation() error {
	my.writeLine("# 变更根类型")
	my.writeLine("type Mutation {")

	// 为每个实体类生成变更字段
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 创建操作
		my.writeLine("  # ", className, "创建")
		my.writeField("create"+className, className, field.NonNull(), field.WithArgs([]field.Argument{
			{Name: "data", Type: className + SUFFIX_CREATE_INPUT + "!"},
		}...))

		// 更新操作
		my.writeLine()
		my.writeLine("  # ", className, "更新")
		my.writeField("update"+className, className, field.NonNull(), field.WithArgs([]field.Argument{
			{Name: "id", Type: SCALAR_ID + "!"},
			{Name: "data", Type: className + SUFFIX_UPDATE_INPUT + "!"},
		}...))

		// 删除操作
		my.writeLine()
		my.writeLine("  # ", className, "删除")
		my.writeField("delete"+className, SCALAR_BOOLEAN, field.NonNull(), field.WithArgs([]field.Argument{
			{Name: "id", Type: SCALAR_ID + "!"},
		}...))

		// 批量删除操作
		my.writeLine()
		my.writeLine("  # ", className, "批量删除")
		my.writeField("delete"+className, SCALAR_INT, field.NonNull(), field.WithArgs([]field.Argument{
			{Name: "filter", Type: className + SUFFIX_FILTER + "!"},
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
	my.writeField("sum", SCALAR_FLOAT, field.WithComment(COMMENT_SUM))
	my.writeField("avg", SCALAR_FLOAT, field.WithComment(COMMENT_AVG))
	my.writeField("min", SCALAR_FLOAT, field.WithComment(COMMENT_MIN))
	my.writeField("max", SCALAR_FLOAT, field.WithComment(COMMENT_MAX))
	my.writeField("count", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_COUNT))
	my.writeField("countDistinct", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_DISTINCT))
	my.writeLine("}")
	my.writeLine()

	// 日期聚合结果
	my.writeLine("# ", DESC_DATE_TIME_STATS)
	my.writeLine("type ", TYPE_DATE_TIME_STATS, " {")
	my.writeField("min", SCALAR_DATE_TIME, field.WithComment(COMMENT_MIN_DATE))
	my.writeField("max", SCALAR_DATE_TIME, field.WithComment(COMMENT_MAX_DATE))
	my.writeField("count", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_COUNT))
	my.writeField("countDistinct", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_DISTINCT))
	my.writeLine("}")
	my.writeLine()

	// 字符串聚合结果
	my.writeLine("# ", DESC_STRING_STATS)
	my.writeLine("type ", TYPE_STRING_STATS, " {")
	my.writeField("min", SCALAR_STRING, field.WithComment(COMMENT_MIN_STRING))
	my.writeField("max", SCALAR_STRING, field.WithComment(COMMENT_MAX_STRING))
	my.writeField("count", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_COUNT))
	my.writeField("countDistinct", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_DISTINCT))
	my.writeLine("}")
	my.writeLine()

	// 为每个实体类生成统计类型
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成统计类型
		my.writeLine("# ", className, "聚合")
		my.writeLine("type ", className, SUFFIX_STATS, " {")
		my.writeField("count", SCALAR_INT, field.NonNull())

		// 添加统计字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 根据字段类型添加对应的统计类型
			typeName := my.getGraphQLType(field)
			switch typeName {
			case SCALAR_INT, SCALAR_FLOAT, SCALAR_ID:
				my.writeField(fieldName, TYPE_NUMBER_STATS)
			case SCALAR_STRING:
				my.writeField(fieldName, TYPE_STRING_STATS)
			case SCALAR_DATE_TIME:
				my.writeField(fieldName, TYPE_DATE_TIME_STATS)
			case SCALAR_BOOLEAN:
				my.writeField(fieldName+"True", SCALAR_INT)
				my.writeField(fieldName+"False", SCALAR_INT)
			}
		}

		// 添加分组聚合
		my.writeLine("  # 分组聚合")
		my.writeField("groupBy", "["+className+SUFFIX_GROUP+"!]")
		my.writeLine("}")
		my.writeLine("")

		// 生成对应的分组类型
		my.writeLine("# ", className, "分组结果")
		my.writeLine("type ", className, SUFFIX_GROUP, " {")
		my.writeField("key", SCALAR_JSON, field.NonNull(), field.WithComment(COMMENT_GROUP_KEY))
		my.writeField("count", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_COUNT))
		my.writeLine("  # 可以包含其他聚合字段")
		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderPaging 渲染分页类型
func (my *Renderer) renderPaging() error {
	my.writeLine("# ", SEPARATOR_LINE, " ", SECTION_CONNECTION, " ", SEPARATOR_LINE, "\n")

	// 为每个实体类生成分页类型
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成分页类型
		my.writeLine("# ", className, "分页结果")
		my.writeLine("type ", className, SUFFIX_PAGE, " {")
		my.writeField("items", className, field.NonNull(), field.ListNonNull(), field.WithComment("直接返回"+className+"对象数组"))
		my.writeField("pageInfo", TYPE_PAGE_INFO, field.NonNull())
		my.writeField("total", SCALAR_INT, field.NonNull())
		my.writeLine("}")
		my.writeLine("")

		// 生成对应的分组类型
		my.writeLine("# ", className, "分组结果")
		my.writeLine("type ", className, SUFFIX_GROUP, " {")
		my.writeField("key", SCALAR_JSON, field.NonNull(), field.WithComment(COMMENT_GROUP_KEY))
		my.writeField("count", SCALAR_INT, field.NonNull(), field.WithComment(COMMENT_COUNT))
		my.writeLine("  # 可以包含其他聚合字段")
		my.writeLine("}")
		my.writeLine()
	}

	return nil
}

// writeField 使用优化的子包渲染字段
func (my *Renderer) writeField(name string, typeName string, options ...field.Option) {
	// 使用子包中的便捷方法生成字段字符串
	fieldStr := field.MakeField(name, typeName, options...)
	my.writeLine(fieldStr)
}
