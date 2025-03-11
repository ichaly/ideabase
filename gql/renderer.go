package gql

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/rs/zerolog/log"
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
	my.sb.Reset()

	// 添加schema版本和说明
	my.writeLine("# IdeaBase GraphQL Schema")
	fmt.Fprintf(my.sb, "# 版本: %s\n\n", my.meta.Version)

	// 渲染标量类型
	if err := my.renderScalars(); err != nil {
		return "", fmt.Errorf("渲染标量类型失败: %w", err)
	}

	// 渲染枚举类型
	if err := my.renderEnums(); err != nil {
		return "", fmt.Errorf("渲染枚举类型失败: %w", err)
	}

	// 渲染通用类型（如PageInfo, Stats类型等）
	if err := my.renderCommon(); err != nil {
		return "", fmt.Errorf("渲染通用类型失败: %w", err)
	}

	// 渲染实体类型
	if err := my.renderTypes(); err != nil {
		return "", fmt.Errorf("渲染实体类型失败: %w", err)
	}

	// 渲染分页类型
	if err := my.renderPaging(); err != nil {
		return "", fmt.Errorf("渲染分页类型失败: %w", err)
	}

	// 渲染统计类型
	if err := my.renderStats(); err != nil {
		return "", fmt.Errorf("渲染统计类型失败: %w", err)
	}

	// 渲染过滤器类型
	if err := my.renderFilter(); err != nil {
		return "", fmt.Errorf("渲染过滤器类型失败: %w", err)
	}

	// 渲染实体过滤器
	if err := my.renderEntity(); err != nil {
		return "", fmt.Errorf("渲染实体过滤器失败: %w", err)
	}

	// 渲染排序类型
	if err := my.renderSort(); err != nil {
		return "", fmt.Errorf("渲染排序类型失败: %w", err)
	}

	// 渲染输入类型
	if err := my.renderInput(); err != nil {
		return "", fmt.Errorf("渲染输入类型失败: %w", err)
	}

	// 渲染查询根类型
	if err := my.renderQuery(); err != nil {
		return "", fmt.Errorf("渲染查询类型失败: %w", err)
	}

	// 渲染变更根类型
	if err := my.renderMutation(); err != nil {
		return "", fmt.Errorf("渲染变更类型失败: %w", err)
	}

	// 保存到文件
	content := my.sb.String()
	if err := my.SaveToFile(content); err != nil {
		return "", fmt.Errorf("保存schema文件失败: %w", err)
	}

	return content, nil
}

// writeLine 写入一行文本（自动添加换行符）
func (my *Renderer) writeLine(text string) {
	my.write(text)
	my.write("\n")
}

// write 直接写入文本
func (my *Renderer) write(text string) {
	my.sb.WriteString(text)
}

// SaveToFile 将生成的Schema保存到文件
func (my *Renderer) SaveToFile(content string) error {
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
	my.writeLine("# 自定义标量类型")
	my.writeLine("scalar Json")
	my.writeLine("scalar Cursor")
	my.writeLine("scalar DateTime")
	my.writeLine("")
	return nil
}

// renderEnums 渲染枚举类型
func (my *Renderer) renderEnums() error {
	// 渲染排序方向枚举
	my.writeLine("# 排序方向枚举")
	my.writeLine("enum SortDirection {")
	my.writeLine("  ASC")
	my.writeLine("  DESC")
	my.writeLine("  ASC_NULLS_FIRST")
	my.writeLine("  DESC_NULLS_FIRST")
	my.writeLine("  ASC_NULLS_LAST")
	my.writeLine("  DESC_NULLS_LAST")
	my.writeLine("}")
	my.writeLine("")

	// 渲染空值条件枚举
	my.writeLine("# 空值条件枚举")
	my.writeLine("enum IsInput {")
	my.writeLine("  NULL")
	my.writeLine("  NOT_NULL")
	my.writeLine("}")
	my.writeLine("")

	return nil
}

// renderCommon 渲染通用类型
func (my *Renderer) renderCommon() error {
	// 渲染分页信息类型
	my.writeLine("# ------------------ 分页相关类型 ------------------\n")
	my.writeLine("# 页面信息（用于游标分页）")
	my.writeLine("type PageInfo {")
	my.writeLine("  hasNext: Boolean!       # 是否有下一页")
	my.writeLine("  hasPrev: Boolean!       # 是否有上一页")
	my.writeLine("  start: Cursor           # 当前页第一条记录的游标")
	my.writeLine("  end: Cursor             # 当前页最后一条记录的游标")
	my.writeLine("}")
	my.writeLine("")

	// 渲染分组选项类型
	my.writeLine("# 聚合分组选项")
	my.writeLine("input GroupBy {")
	my.writeLine("  fields: [String!]!  # 分组字段")
	my.writeLine("  having: Json        # 分组过滤条件")
	my.writeLine("  limit: Int          # 分组结果限制")
	my.writeLine("  sort: Json          # 分组结果排序")
	my.writeLine("}")
	my.writeLine("")

	return nil
}

// renderTypes 渲染所有实体类型定义
func (my *Renderer) renderTypes() error {
	// 遍历所有类定义，确保只使用类名作为键
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 添加类型描述
		if class.Description != "" {
			my.writeLine("# " + class.Description)
		}

		// 开始类型定义
		my.writeLine("type " + className + " {")

		// 添加所有字段，确保只处理真正的字段名
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 获取GraphQL字段类型
			typeName := my.mapDBTypeToGraphQL(field.Type)
			// 非空字段添加!
			if !field.Nullable {
				typeName += "!"
			}

			// 添加描述作为注释
			if field.Description != "" {
				my.writeLine("  # " + field.Description)
			}

			// 输出字段定义
			my.writeLine("  " + fieldName + ": " + typeName)
		}

		// 添加关系字段
		if err := my.renderRelation(className, class); err != nil {
			return err
		}

		// 结束类型定义
		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderRelation 渲染关系字段
func (my *Renderer) renderRelation(className string, class *internal.Class) error {
	// 目前暂无关系信息，需要实际项目中实现
	// 以下是示例代码，实际使用时需要调整为正确的关系数据结构

	// 如果meta.Relations还未定义，这里简单返回，不输出关系信息
	// TODO: 实现从元数据中获取关系信息的逻辑

	// 假设我们的关系数据是从其他地方获取的
	// 这里可以添加具体实现

	log.Debug().Str("class", className).Msg("处理实体关系")
	return nil
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
		my.writeLine("# " + className + "创建输入")
		my.writeLine("input " + className + "CreateInput {")
		// 添加创建时的必要字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			typeName := my.mapDBTypeToGraphQL(field.Type)
			// 非空字段添加!
			if !field.Nullable {
				typeName += "!"
			}

			my.writeLine("  " + fieldName + ": " + typeName)
		}
		my.writeLine("}")
		my.writeLine("")

		// 生成更新输入类型
		my.writeLine("# " + className + "更新输入")
		my.writeLine("input " + className + "UpdateInput {")
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
			my.writeLine("  " + fieldName + ": " + typeName)
		}
		my.writeLine("}")
		my.writeLine("")
	}

	// 添加关系输入类型
	my.writeLine("# 关联操作")
	my.writeLine("input ConnectInput {")
	my.writeLine("  id: ID!")
	my.writeLine("}")
	my.writeLine("")

	my.writeLine("# 关系操作")
	my.writeLine("input RelationInput {")
	my.writeLine("  connect: [ID!]")
	my.writeLine("  disconnect: [ID!]")
	my.writeLine("}")
	my.writeLine("")

	return nil
}

// renderFilter 渲染基础过滤器类型
func (my *Renderer) renderFilter() error {
	my.writeLine("# ------------------ 过滤器类型定义 ------------------\n")

	// 定义过滤器映射表，每种类型支持的操作
	for scalarType, operators := range symbols {
		filterName := scalarType + "Filter"
		my.writeLine("# " + scalarType + "过滤器")
		my.writeLine("input " + filterName + " {")

		// 渲染该类型支持的所有操作符
		for _, op := range operators {
			if op.Name == HAS_KEY || op.Name == HAS_KEY_ANY || op.Name == HAS_KEY_ALL {
				my.writeLine(fmt.Sprintf("  %s: %s # %s", op.Name, "String", op.Desc))
			} else if op.Name == IN || op.Name == NI {
				my.writeLine(fmt.Sprintf("  %s: [%s!] # %s", op.Name, scalarType, op.Desc))
			} else if op.Name == IS {
				my.writeLine(fmt.Sprintf("  %s: %s # %s", op.Name, "IsInput", op.Desc))
			} else {
				my.writeLine(fmt.Sprintf("  %s: %s # %s", op.Name, scalarType, op.Desc))
			}
		}

		my.writeLine("}")
		my.writeLine("")
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
		my.writeLine("# " + className + "查询条件")
		my.writeLine("input " + className + "Filter {")

		// 添加字段过滤条件
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 根据字段类型添加对应的过滤器
			typeName := my.mapDBTypeToGraphQL(field.Type)
			my.writeLine("  " + fieldName + ": " + typeName + "Filter")
		}

		// 添加布尔逻辑操作符
		my.writeLine("  " + NOT + ": " + className + "Filter")
		my.writeLine("  " + AND + ": [" + className + "Filter!]")
		my.writeLine("  " + OR + ": [" + className + "Filter!]")

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
		my.writeLine("# " + className + "排序")
		my.writeLine("input " + className + "Sort {")

		// 添加可排序字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 判断字段是否可排序
			my.writeLine("  " + fieldName + ": SortDirection")
		}

		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderQuery 渲染查询根类型
func (my *Renderer) renderQuery() error {
	my.writeLine("# ------------------ 查询和变更 ------------------\n")
	my.writeLine("# 查询根类型")
	my.writeLine("type Query {")

	// 为每个实体类生成查询字段
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 单个实体查询
		my.writeLine("  # 单个" + className + "查询")
		my.writeLine("  " + className + "(id: ID!): " + className)

		// 统一列表查询（支持两种分页方式）
		my.writeLine("")
		my.writeLine("  # " + className + "列表查询")
		my.writeLine("  " + className + "(")
		my.writeLine("    filter: " + className + "Filter")
		my.writeLine("    sort: [" + className + "Sort!]")
		my.writeLine("    # 传统分页参数")
		my.writeLine("    limit: Int")
		my.writeLine("    offset: Int")
		my.writeLine("    # 游标分页参数")
		my.writeLine("    first: Int")
		my.writeLine("    after: Cursor")
		my.writeLine("    last: Int")
		my.writeLine("    before: Cursor")
		my.writeLine("  ): " + className + "Page!")

		// 高级聚合查询
		my.writeLine("")
		my.writeLine("  # " + className + "聚合查询")
		my.writeLine("  " + className + "Stats(")
		my.writeLine("    filter: " + className + "Filter")
		my.writeLine("    groupBy: GroupBy")
		my.writeLine("  ): " + className + "Stats!")
	}

	my.writeLine("}")
	my.writeLine("")
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
		my.writeLine("  # 创建" + className)
		my.writeLine("  create" + className + "(data: " + className + "CreateInput!): " + className + "!")

		// 更新操作
		my.writeLine("")
		my.writeLine("  # 更新" + className)
		my.writeLine("  update" + className + "(id: ID!, data: " + className + "UpdateInput!): " + className + "!")

		// 删除操作
		my.writeLine("")
		my.writeLine("  # 删除" + className)
		my.writeLine("  delete" + className + "(id: ID!): Boolean!")

		// 批量删除操作
		my.writeLine("")
		my.writeLine("  # 批量删除" + className)
		my.writeLine("  delete" + className + "(filter: " + className + "Filter!): Int!")
	}

	my.writeLine("}")
	return nil
}

// renderStats 渲染统计类型
func (my *Renderer) renderStats() error {
	my.writeLine("# ------------------ 聚合函数相关类型 ------------------\n")

	// 数值聚合结果
	my.writeLine("# 数值聚合结果")
	my.writeLine("type NumberStats {")
	my.writeLine("  sum: Float              # 总和")
	my.writeLine("  avg: Float              # 平均值")
	my.writeLine("  min: Float              # 最小值")
	my.writeLine("  max: Float              # 最大值")
	my.writeLine("  count: Int!             # 计数")
	my.writeLine("  countDistinct: Int!     # 去重计数")
	my.writeLine("}")
	my.writeLine("")

	// 日期聚合结果
	my.writeLine("# 日期聚合结果")
	my.writeLine("type DateTimeStats {")
	my.writeLine("  min: DateTime           # 最早时间")
	my.writeLine("  max: DateTime           # 最晚时间")
	my.writeLine("  count: Int!             # 计数")
	my.writeLine("  countDistinct: Int!     # 去重计数")
	my.writeLine("}")
	my.writeLine("")

	// 字符串聚合结果
	my.writeLine("# 字符串聚合结果")
	my.writeLine("type StringStats {")
	my.writeLine("  min: String             # 最小值(按字典序)")
	my.writeLine("  max: String             # 最大值(按字典序)")
	my.writeLine("  count: Int!             # 计数")
	my.writeLine("  countDistinct: Int!     # 去重计数")
	my.writeLine("}")
	my.writeLine("")

	// 为每个实体类生成统计类型
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成统计类型
		my.writeLine("# " + className + "聚合")
		my.writeLine("type " + className + "Stats {")
		my.writeLine("  count: Int!")

		// 添加统计字段
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 根据字段类型添加对应的统计函数
			typeName := my.mapDBTypeToGraphQL(field.Type)
			switch typeName {
			case SCALAR_INT, SCALAR_FLOAT, SCALAR_ID:
				my.writeLine("  " + fieldName + ": NumberStats")
			case SCALAR_STRING:
				my.writeLine("  " + fieldName + ": StringStats")
			case SCALAR_DATE_TIME:
				my.writeLine("  " + fieldName + ": DateTimeStats")
			case SCALAR_BOOLEAN:
				my.writeLine("  " + fieldName + "True: Int")
				my.writeLine("  " + fieldName + "False: Int")
			}
		}

		// 添加分组聚合
		my.writeLine("  # 分组聚合")
		my.writeLine("  groupBy: [" + className + "Group!]")
		my.writeLine("}")
		my.writeLine("")

		// 生成对应的分组类型
		my.writeLine("# " + className + "分组结果")
		my.writeLine("type " + className + "Group {")
		my.writeLine("  key: Json!          # 分组键")
		my.writeLine("  count: Int!")
		my.writeLine("  aggregate: [String!]   # 聚合函数列表")
		my.writeLine("  distinct: [String!]    # 去重字段列表")
		my.writeLine("  having: Json        # 分组过滤条件")
		my.writeLine("  limit: Int            # 分组结果限制数量")
		my.writeLine("  sort: Json          # 分组结果排序")
		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// renderPaging 渲染分页类型
func (my *Renderer) renderPaging() error {
	my.writeLine("# ------------------ 连接和边类型（游标分页） ------------------\n")

	// 为每个实体类生成分页类型
	for className, class := range my.meta.Nodes {
		// 确保只处理真正的类名，跳过表名索引
		if className != class.Name {
			continue
		}

		// 生成分页类型
		my.writeLine("# " + className + "分页结果")
		my.writeLine("type " + className + "Page {")
		my.writeLine("  items: [" + className + "!]!         # 直接返回" + className + "对象数组")
		my.writeLine("  pageInfo: PageInfo!")
		my.writeLine("  total: Int!")
		my.writeLine("}")
		my.writeLine("")
	}

	return nil
}

// mapDBTypeToGraphQL 将数据库类型映射为GraphQL类型
func (my *Renderer) mapDBTypeToGraphQL(dbType string) string {
	// 根据数据库类型返回相应的GraphQL类型
	dbType = strings.ToLower(dbType)

	// 优先使用配置中的映射
	if my.meta != nil && my.meta.cfg != nil && my.meta.cfg.Schema.TypeMapping != nil {
		if mapping, ok := my.meta.cfg.Schema.TypeMapping[dbType]; ok {
			return mapping
		}
	}

	return SCALAR_STRING
}
