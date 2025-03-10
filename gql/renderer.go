package gql

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/rs/zerolog/log"
)

// Renderer 负责将元数据渲染为GraphQL schema
type Renderer struct {
	meta *Metadata
}

// NewRenderer 创建新的Schema渲染器
func NewRenderer(meta *Metadata) *Renderer {
	return &Renderer{
		meta: meta,
	}
}

// Generate 生成完整的GraphQL schema
func (my *Renderer) Generate() (string, error) {
	var buf bytes.Buffer

	// 添加schema版本和说明
	fmt.Fprintf(&buf, "# IdeaBase GraphQL Schema\n")
	fmt.Fprintf(&buf, "# 版本: %s\n\n", my.meta.Version)

	// 渲染标量类型
	if err := my.renderScalars(&buf); err != nil {
		return "", fmt.Errorf("渲染标量类型失败: %w", err)
	}

	// 渲染枚举类型
	if err := my.renderEnums(&buf); err != nil {
		return "", fmt.Errorf("渲染枚举类型失败: %w", err)
	}

	// 渲染通用类型（如PageInfo, Stats类型等）
	if err := my.renderCommon(&buf); err != nil {
		return "", fmt.Errorf("渲染通用类型失败: %w", err)
	}

	// 渲染实体类型
	if err := my.renderTypes(&buf); err != nil {
		return "", fmt.Errorf("渲染实体类型失败: %w", err)
	}

	// 渲染分页类型
	if err := my.renderPaging(&buf); err != nil {
		return "", fmt.Errorf("渲染分页类型失败: %w", err)
	}

	// 渲染统计类型
	if err := my.renderStats(&buf); err != nil {
		return "", fmt.Errorf("渲染统计类型失败: %w", err)
	}

	// 渲染过滤器类型
	if err := my.renderFilter(&buf); err != nil {
		return "", fmt.Errorf("渲染过滤器类型失败: %w", err)
	}

	// 渲染实体过滤器
	if err := my.renderEntity(&buf); err != nil {
		return "", fmt.Errorf("渲染实体过滤器失败: %w", err)
	}

	// 渲染排序类型
	if err := my.renderSort(&buf); err != nil {
		return "", fmt.Errorf("渲染排序类型失败: %w", err)
	}

	// 渲染输入类型
	if err := my.renderInput(&buf); err != nil {
		return "", fmt.Errorf("渲染输入类型失败: %w", err)
	}

	// 渲染查询根类型
	if err := my.renderQuery(&buf); err != nil {
		return "", fmt.Errorf("渲染查询类型失败: %w", err)
	}

	// 渲染变更根类型
	if err := my.renderMutation(&buf); err != nil {
		return "", fmt.Errorf("渲染变更类型失败: %w", err)
	}

	// 保存到文件
	if err := my.SaveToFile(buf.String()); err != nil {
		return "", fmt.Errorf("保存schema文件失败: %w", err)
	}

	return buf.String(), nil
}

// SaveToFile 将生成的Schema保存到文件
func (my *Renderer) SaveToFile(content string) error {
	filePath := my.meta.cfg.Root + "/cfg/schema.graphql"

	// 确保路径有效
	if filePath == "" {
		return fmt.Errorf("未指定保存路径，且无法从配置中获取有效路径")
	}

	// 保存文件
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("保存Schema文件失败: %w", err)
	}

	log.Info().Str("path", filePath).Msg("Schema成功保存到文件")
	return nil
}

// renderScalars 渲染标量类型
func (my *Renderer) renderScalars(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "# 自定义标量类型\n")
	fmt.Fprintf(buf, "scalar JSON\n")
	fmt.Fprintf(buf, "scalar Cursor\n")
	fmt.Fprintf(buf, "scalar DateTime\n")
	fmt.Fprintf(buf, "\n")
	return nil
}

// renderEnums 渲染枚举类型
func (my *Renderer) renderEnums(buf *bytes.Buffer) error {
	// 渲染排序方向枚举
	fmt.Fprintf(buf, "# 排序方向枚举，包含NULL值处理\n")
	fmt.Fprintf(buf, "enum SortDirection {\n")
	fmt.Fprintf(buf, "  ASC\n")
	fmt.Fprintf(buf, "  DESC\n")
	fmt.Fprintf(buf, "  ASC_NULLS_FIRST\n")
	fmt.Fprintf(buf, "  DESC_NULLS_FIRST\n")
	fmt.Fprintf(buf, "  ASC_NULLS_LAST\n")
	fmt.Fprintf(buf, "  DESC_NULLS_LAST\n")
	fmt.Fprintf(buf, "}\n\n")

	// 渲染空值条件枚举
	fmt.Fprintf(buf, "# 空值条件枚举\n")
	fmt.Fprintf(buf, "enum IsInput {\n")
	fmt.Fprintf(buf, "  NULL\n")
	fmt.Fprintf(buf, "  NOT_NULL\n")
	fmt.Fprintf(buf, "}\n\n")

	return nil
}

// renderCommon 渲染通用类型
func (my *Renderer) renderCommon(buf *bytes.Buffer) error {
	// 渲染分页信息类型
	fmt.Fprintf(buf, "# ------------------ 分页相关类型 ------------------\n\n")
	fmt.Fprintf(buf, "# 页面信息（用于游标分页）\n")
	fmt.Fprintf(buf, "type PageInfo {\n")
	fmt.Fprintf(buf, "  hasNext: Boolean!       # 是否有下一页\n")
	fmt.Fprintf(buf, "  hasPrev: Boolean!       # 是否有上一页\n")
	fmt.Fprintf(buf, "  start: Cursor           # 当前页第一条记录的游标\n")
	fmt.Fprintf(buf, "  end: Cursor             # 当前页最后一条记录的游标\n")
	fmt.Fprintf(buf, "}\n\n")

	// 渲染分组选项类型
	fmt.Fprintf(buf, "# 聚合分组选项\n")
	fmt.Fprintf(buf, "input GroupBy {\n")
	fmt.Fprintf(buf, "  fields: [String!]!  # 分组字段\n")
	fmt.Fprintf(buf, "  having: JSON        # 分组过滤条件\n")
	fmt.Fprintf(buf, "  limit: Int          # 分组结果限制\n")
	fmt.Fprintf(buf, "  sort: JSON          # 分组结果排序\n")
	fmt.Fprintf(buf, "}\n\n")

	return nil
}

// renderTypes 渲染所有实体类型定义
func (my *Renderer) renderTypes(buf *bytes.Buffer) error {
	// 遍历所有类定义，确保只使用类名作为键
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 添加类型描述
		if class.Description != "" {
			fmt.Fprintf(buf, "# %s\n", class.Description)
		}

		// 开始类型定义
		fmt.Fprintf(buf, "type %s {\n", className)

		// 添加所有字段，确保只处理真正的字段名
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			typeName := my.mapDBTypeToGraphQL(field.Type)

			// 处理非空标记
			if !field.Nullable {
				typeName = typeName + "!"
			}

			// 添加描述作为注释
			if field.Description != "" {
				fmt.Fprintf(buf, "  # %s\n", field.Description)
			}

			// 输出字段定义
			fmt.Fprintf(buf, "  %s: %s\n", fieldName, typeName)
		}

		// 添加关系字段
		if err := my.renderRelation(buf, className, class); err != nil {
			return err
		}

		// 结束类型定义
		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderRelation 渲染关系字段
func (my *Renderer) renderRelation(buf *bytes.Buffer, className string, class *internal.Class) error {
	// 目前暂无关系信息，需要实际项目中实现
	// 以下是示例代码，实际使用时需要调整为正确的关系数据结构

	// 如果meta.Relations还未定义，这里简单返回，不输出关系信息
	// TODO: 实现从元数据中获取关系信息的逻辑

	// 假设我们的关系数据是从其他地方获取的
	// 这里可以添加具体实现

	log.Debug().Str("class", className).Msg("处理实体关系")
	return nil
}

// pluralize 简单的英文复数形式转换
func pluralize(word string) string {
	// 简化版，仅处理常见情况
	if strings.HasSuffix(word, "s") ||
		strings.HasSuffix(word, "x") ||
		strings.HasSuffix(word, "z") ||
		strings.HasSuffix(word, "ch") ||
		strings.HasSuffix(word, "sh") {
		return word + "es"
	}
	if strings.HasSuffix(word, "y") {
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}

// renderInput 渲染输入类型
func (my *Renderer) renderInput(buf *bytes.Buffer) error {
	// 为每个实体类生成创建和更新输入类型
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 生成创建输入类型
		fmt.Fprintf(buf, "input %sCreateInput {\n", className)
		// 添加创建时的必要字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 排除自动生成的字段
			if strings.EqualFold(fieldName, "id") ||
				strings.EqualFold(fieldName, "createdAt") ||
				strings.EqualFold(fieldName, "updatedAt") {
				continue
			}

			typeName := my.mapDBTypeToGraphQL(field.Type)
			// 非空字段添加!
			if !field.Nullable {
				typeName += "!"
			}

			fmt.Fprintf(buf, "  %s: %s\n", fieldName, typeName)
		}
		fmt.Fprintf(buf, "}\n\n")

		// 生成更新输入类型
		fmt.Fprintf(buf, "input %sUpdateInput {\n", className)
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

			typeName := my.mapDBTypeToGraphQL(field.Type)
			// 更新时所有字段都是可选的
			fmt.Fprintf(buf, "  %s: %s\n", fieldName, typeName)
		}
		fmt.Fprintf(buf, "}\n\n")
	}

	// 添加关系输入类型
	fmt.Fprintf(buf, "# 关联操作\n")
	fmt.Fprintf(buf, "input ConnectInput {\n")
	fmt.Fprintf(buf, "  id: ID!\n")
	fmt.Fprintf(buf, "}\n\n")

	fmt.Fprintf(buf, "# 关系操作\n")
	fmt.Fprintf(buf, "input RelationInput {\n")
	fmt.Fprintf(buf, "  connect: [ID!]\n")
	fmt.Fprintf(buf, "  disconnect: [ID!]\n")
	fmt.Fprintf(buf, "}\n\n")

	return nil
}

// renderFilter 渲染基础过滤器类型
func (my *Renderer) renderFilter(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "# ------------------ 过滤器类型定义 ------------------\n\n")

	// 字符串过滤器
	fmt.Fprintf(buf, "# 字符串过滤器\n")
	fmt.Fprintf(buf, "input StringFilter {\n")
	fmt.Fprintf(buf, "  eq: String        # 等于\n")
	fmt.Fprintf(buf, "  ne: String        # 不等于\n")
	fmt.Fprintf(buf, "  gt: String        # 大于\n")
	fmt.Fprintf(buf, "  ge: String        # 大于等于\n")
	fmt.Fprintf(buf, "  lt: String        # 小于\n")
	fmt.Fprintf(buf, "  le: String        # 小于等于\n")
	fmt.Fprintf(buf, "  in: [String!]     # 在列表中\n")
	fmt.Fprintf(buf, "  ni: [String!]     # 不在列表中\n")
	fmt.Fprintf(buf, "  like: String      # 模糊匹配(区分大小写)\n")
	fmt.Fprintf(buf, "  ilike: String     # 模糊匹配(不区分大小写)\n")
	fmt.Fprintf(buf, "  regex: String     # 正则表达式匹配\n")
	fmt.Fprintf(buf, "  iregex: String    # 正则表达式匹配(不区分大小写)\n")
	fmt.Fprintf(buf, "  is: IsInput       # 是否为NULL\n")
	fmt.Fprintf(buf, "}\n\n")

	// 整数过滤器
	fmt.Fprintf(buf, "# 整数过滤器\n")
	fmt.Fprintf(buf, "input IntFilter {\n")
	fmt.Fprintf(buf, "  eq: Int\n")
	fmt.Fprintf(buf, "  ne: Int\n")
	fmt.Fprintf(buf, "  gt: Int\n")
	fmt.Fprintf(buf, "  ge: Int\n")
	fmt.Fprintf(buf, "  lt: Int\n")
	fmt.Fprintf(buf, "  le: Int\n")
	fmt.Fprintf(buf, "  in: [Int!]\n")
	fmt.Fprintf(buf, "  ni: [Int!]\n")
	fmt.Fprintf(buf, "  is: IsInput\n")
	fmt.Fprintf(buf, "}\n\n")

	// 浮点数过滤器
	fmt.Fprintf(buf, "# 浮点数过滤器\n")
	fmt.Fprintf(buf, "input FloatFilter {\n")
	fmt.Fprintf(buf, "  eq: Float\n")
	fmt.Fprintf(buf, "  ne: Float\n")
	fmt.Fprintf(buf, "  gt: Float\n")
	fmt.Fprintf(buf, "  ge: Float\n")
	fmt.Fprintf(buf, "  lt: Float\n")
	fmt.Fprintf(buf, "  le: Float\n")
	fmt.Fprintf(buf, "  in: [Float!]\n")
	fmt.Fprintf(buf, "  ni: [Float!]\n")
	fmt.Fprintf(buf, "  is: IsInput\n")
	fmt.Fprintf(buf, "}\n\n")

	// 布尔过滤器
	fmt.Fprintf(buf, "# 布尔过滤器\n")
	fmt.Fprintf(buf, "input BoolFilter {\n")
	fmt.Fprintf(buf, "  eq: Boolean\n")
	fmt.Fprintf(buf, "  is: IsInput\n")
	fmt.Fprintf(buf, "}\n\n")

	// 日期时间过滤器
	fmt.Fprintf(buf, "# 日期时间过滤器\n")
	fmt.Fprintf(buf, "input DateTimeFilter {\n")
	fmt.Fprintf(buf, "  eq: DateTime\n")
	fmt.Fprintf(buf, "  ne: DateTime\n")
	fmt.Fprintf(buf, "  gt: DateTime\n")
	fmt.Fprintf(buf, "  ge: DateTime\n")
	fmt.Fprintf(buf, "  lt: DateTime\n")
	fmt.Fprintf(buf, "  le: DateTime\n")
	fmt.Fprintf(buf, "  in: [DateTime!]\n")
	fmt.Fprintf(buf, "  ni: [DateTime!]\n")
	fmt.Fprintf(buf, "  is: IsInput\n")
	fmt.Fprintf(buf, "}\n\n")

	// ID过滤器
	fmt.Fprintf(buf, "# ID过滤器\n")
	fmt.Fprintf(buf, "input IDFilter {\n")
	fmt.Fprintf(buf, "  eq: ID\n")
	fmt.Fprintf(buf, "  ne: ID\n")
	fmt.Fprintf(buf, "  in: [ID!]\n")
	fmt.Fprintf(buf, "  ni: [ID!]\n")
	fmt.Fprintf(buf, "  is: IsInput\n")
	fmt.Fprintf(buf, "}\n\n")

	// JSON过滤器
	fmt.Fprintf(buf, "# JSON过滤器\n")
	fmt.Fprintf(buf, "input JSONFilter {\n")
	fmt.Fprintf(buf, "  eq: JSON\n")
	fmt.Fprintf(buf, "  ne: JSON\n")
	fmt.Fprintf(buf, "  is: IsInput\n")
	fmt.Fprintf(buf, "  hasKey: String      # 判断JSON是否包含特定键\n")
	fmt.Fprintf(buf, "  hasKeyAny: [String!] # 判断JSON是否包含任意一个键\n")
	fmt.Fprintf(buf, "  hasKeyAll: [String!] # 判断JSON是否包含所有键\n")
	fmt.Fprintf(buf, "}\n\n")

	return nil
}

// renderEntity 渲染实体过滤器
func (my *Renderer) renderEntity(buf *bytes.Buffer) error {
	// 为每个实体类生成过滤器
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 生成过滤器类型
		fmt.Fprintf(buf, "# %s查询条件\n", className)
		fmt.Fprintf(buf, "input %sFilter {\n", className)

		// 添加字段过滤条件
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 根据字段类型添加对应的过滤器
			typeName := my.mapDBTypeToGraphQL(field.Type)
			switch typeName {
			case "ID":
				fmt.Fprintf(buf, "  %s: IDFilter\n", fieldName)
			case "String":
				fmt.Fprintf(buf, "  %s: StringFilter\n", fieldName)
			case "Int":
				fmt.Fprintf(buf, "  %s: IntFilter\n", fieldName)
			case "Float":
				fmt.Fprintf(buf, "  %s: FloatFilter\n", fieldName)
			case "Boolean":
				fmt.Fprintf(buf, "  %s: BoolFilter\n", fieldName)
			case "DateTime":
				fmt.Fprintf(buf, "  %s: DateTimeFilter\n", fieldName)
			case "JSON":
				fmt.Fprintf(buf, "  %s: JSONFilter\n", fieldName)
			}
		}

		// 添加布尔逻辑操作符
		fmt.Fprintf(buf, "  AND: [%sFilter!]\n", className)
		fmt.Fprintf(buf, "  OR: [%sFilter!]\n", className)
		fmt.Fprintf(buf, "  NOT: %sFilter\n", className)
		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderSort 渲染排序类型
func (my *Renderer) renderSort(buf *bytes.Buffer) error {
	// 为每个实体类生成排序类型
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 生成排序类型
		fmt.Fprintf(buf, "# %s排序\n", className)
		fmt.Fprintf(buf, "input %sSort {\n", className)

		// 添加可排序字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 判断字段是否可排序
			fmt.Fprintf(buf, "  %s: SortDirection\n", fieldName)
		}

		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderQuery 渲染查询根类型
func (my *Renderer) renderQuery(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "# ------------------ 查询和变更 ------------------\n\n")
	fmt.Fprintf(buf, "# 查询根类型\n")
	fmt.Fprintf(buf, "type Query {\n")

	// 为每个实体类生成查询字段
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 单个实体查询
		fmt.Fprintf(buf, "  # 单个%s查询\n", className)
		fmt.Fprintf(buf, "  %s(id: ID!): %s\n",
			strcase.ToLowerCamel(className), className)

		// 统一列表查询（支持两种分页方式）
		fmt.Fprintf(buf, "\n  # %s列表查询\n", className)
		fmt.Fprintf(buf, "  %s(\n",
			strcase.ToLowerCamel(pluralize(className)))
		fmt.Fprintf(buf, "    filter: %sFilter\n", className)
		fmt.Fprintf(buf, "    sort: [%sSort!]\n", className)
		fmt.Fprintf(buf, "    # 传统分页参数\n")
		fmt.Fprintf(buf, "    limit: Int\n")
		fmt.Fprintf(buf, "    offset: Int\n")
		fmt.Fprintf(buf, "    # 游标分页参数\n")
		fmt.Fprintf(buf, "    first: Int\n")
		fmt.Fprintf(buf, "    after: Cursor\n")
		fmt.Fprintf(buf, "    last: Int\n")
		fmt.Fprintf(buf, "    before: Cursor\n")
		fmt.Fprintf(buf, "  ): %sPage!\n", className)

		// 高级聚合查询
		fmt.Fprintf(buf, "\n  # %s聚合查询\n", className)
		fmt.Fprintf(buf, "  %sStats(\n",
			strcase.ToLowerCamel(pluralize(className)))
		fmt.Fprintf(buf, "    filter: %sFilter\n", className)
		fmt.Fprintf(buf, "    groupBy: GroupBy\n")
		fmt.Fprintf(buf, "  ): %sStats!\n", className)
	}

	fmt.Fprintf(buf, "}\n\n")
	return nil
}

// renderMutation 渲染变更根类型
func (my *Renderer) renderMutation(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "# 变更根类型\n")
	fmt.Fprintf(buf, "type Mutation {\n")

	// 为每个实体类生成变更字段
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 创建操作
		fmt.Fprintf(buf, "  # 创建%s\n", className)
		fmt.Fprintf(buf, "  create%s(data: %sCreateInput!): %s!\n",
			className, className, className)

		// 更新操作
		fmt.Fprintf(buf, "\n  # 更新%s\n", className)
		fmt.Fprintf(buf, "  update%s(id: ID!, data: %sUpdateInput!): %s!\n",
			className, className, className)

		// 删除操作
		fmt.Fprintf(buf, "\n  # 删除%s\n", className)
		fmt.Fprintf(buf, "  delete%s(id: ID!): Boolean!\n", className)

		// 批量删除操作
		fmt.Fprintf(buf, "\n  # 批量删除%s\n", className)
		fmt.Fprintf(buf, "  delete%s(filter: %sFilter!): Int!\n",
			pluralize(className), className)
	}

	fmt.Fprintf(buf, "}\n")
	return nil
}

// renderStats 渲染统计类型
func (my *Renderer) renderStats(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "# ------------------ 聚合函数相关类型 ------------------\n\n")

	// 数值聚合结果
	fmt.Fprintf(buf, "# 数值聚合结果\n")
	fmt.Fprintf(buf, "type NumStats {\n")
	fmt.Fprintf(buf, "  sum: Float              # 总和\n")
	fmt.Fprintf(buf, "  avg: Float              # 平均值\n")
	fmt.Fprintf(buf, "  min: Float              # 最小值\n")
	fmt.Fprintf(buf, "  max: Float              # 最大值\n")
	fmt.Fprintf(buf, "  count: Int!             # 计数\n")
	fmt.Fprintf(buf, "  countDistinct: Int!     # 去重计数\n")
	fmt.Fprintf(buf, "}\n\n")

	// 日期聚合结果
	fmt.Fprintf(buf, "# 日期聚合结果\n")
	fmt.Fprintf(buf, "type DateStats {\n")
	fmt.Fprintf(buf, "  min: DateTime           # 最早时间\n")
	fmt.Fprintf(buf, "  max: DateTime           # 最晚时间\n")
	fmt.Fprintf(buf, "  count: Int!             # 计数\n")
	fmt.Fprintf(buf, "  countDistinct: Int!     # 去重计数\n")
	fmt.Fprintf(buf, "}\n\n")

	// 字符串聚合结果
	fmt.Fprintf(buf, "# 字符串聚合结果\n")
	fmt.Fprintf(buf, "type StrStats {\n")
	fmt.Fprintf(buf, "  min: String             # 最小值(按字典序)\n")
	fmt.Fprintf(buf, "  max: String             # 最大值(按字典序)\n")
	fmt.Fprintf(buf, "  count: Int!             # 计数\n")
	fmt.Fprintf(buf, "  countDistinct: Int!     # 去重计数\n")
	fmt.Fprintf(buf, "}\n\n")

	// 为每个实体类生成统计类型
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 生成统计类型
		fmt.Fprintf(buf, "# %s聚合\n", className)
		fmt.Fprintf(buf, "type %sStats {\n", className)
		fmt.Fprintf(buf, "  count: Int!\n")

		// 添加统计字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 根据字段类型添加对应的统计类型
			typeName := my.mapDBTypeToGraphQL(field.Type)
			switch typeName {
			case "Int", "Float", "ID":
				fmt.Fprintf(buf, "  %s: NumStats\n", fieldName)
			case "String":
				fmt.Fprintf(buf, "  %s: StrStats\n", fieldName)
			case "DateTime":
				fmt.Fprintf(buf, "  %s: DateStats\n", fieldName)
			case "Boolean":
				fmt.Fprintf(buf, "  %sTrue: Int\n", fieldName)
				fmt.Fprintf(buf, "  %sFalse: Int\n", fieldName)
			}
		}

		// 添加分组聚合
		fmt.Fprintf(buf, "  # 分组聚合\n")
		fmt.Fprintf(buf, "  groupBy: [%sGroup!]\n", className)
		fmt.Fprintf(buf, "}\n\n")

		// 生成对应的分组类型
		fmt.Fprintf(buf, "# %s分组结果\n", className)
		fmt.Fprintf(buf, "type %sGroup {\n", className)
		fmt.Fprintf(buf, "  key: JSON!          # 分组键\n")
		fmt.Fprintf(buf, "  count: Int!         # 计数\n")
		fmt.Fprintf(buf, "  # 可以包含其他聚合字段\n")
		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderPaging 渲染分页类型
func (my *Renderer) renderPaging(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "# ------------------ 连接和边类型（游标分页） ------------------\n\n")

	// 为每个实体类生成分页类型
	processedClasses := make(map[string]bool)

	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 避免重复处理
		if processedClasses[className] {
			continue
		}
		processedClasses[className] = true

		// 生成分页类型
		fmt.Fprintf(buf, "# %s分页结果\n", className)
		fmt.Fprintf(buf, "type %sPage {\n", className)
		fmt.Fprintf(buf, "  items: [%s!]!         # 直接返回%s对象数组\n", className, className)
		fmt.Fprintf(buf, "  pageInfo: PageInfo!     # 包含边界游标信息\n")
		fmt.Fprintf(buf, "  total: Int!\n")
		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// 辅助方法：将数据库类型映射到GraphQL类型
func (my *Renderer) mapDBTypeToGraphQL(dbType string) string {
	// 处理常见的数据库类型到GraphQL标量类型的映射
	switch strings.ToLower(dbType) {
	case "int", "integer", "smallint", "mediumint", "bigint":
		return "Int"
	case "float", "double", "decimal", "numeric", "real":
		return "Float"
	case "boolean", "bool", "tinyint(1)":
		return "Boolean"
	case "varchar", "char", "text", "tinytext", "mediumtext", "longtext":
		return "String"
	case "date", "datetime", "timestamp":
		return "DateTime"
	case "json", "jsonb":
		return "JSON"
	case "uuid":
		return "ID"
	default:
		// 默认使用String类型
		if strings.Contains(strings.ToLower(dbType), "id") {
			return "ID"
		}
		return "String"
	}
}
