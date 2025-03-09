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

	// 渲染实体类型
	if err := my.renderTypes(&buf); err != nil {
		return "", fmt.Errorf("渲染实体类型失败: %w", err)
	}

	// 渲染输入类型
	if err := my.renderInput(&buf); err != nil {
		return "", fmt.Errorf("渲染输入类型失败: %w", err)
	}

	// 渲染过滤器类型
	if err := my.renderFilter(&buf); err != nil {
		return "", fmt.Errorf("渲染过滤器类型失败: %w", err)
	}

	// 渲染排序类型
	if err := my.renderOrder(&buf); err != nil {
		return "", fmt.Errorf("渲染排序类型失败: %w", err)
	}

	// 渲染查询根类型
	if err := my.renderQuery(&buf); err != nil {
		return "", fmt.Errorf("渲染查询类型失败: %w", err)
	}

	// 渲染变更根类型
	if err := my.renderMutation(&buf); err != nil {
		return "", fmt.Errorf("渲染变更类型失败: %w", err)
	}

	my.SaveToFile(buf.String())

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
	// 标准标量: ID, String, Int, Float, Boolean
	// 添加自定义标量
	fmt.Fprintf(buf, "scalar DateTime\n")
	fmt.Fprintf(buf, "scalar JSON\n\n")
	return nil
}

// renderEnums 渲染枚举类型
func (my *Renderer) renderEnums(buf *bytes.Buffer) error {
	// 为简化起见，此实现暂不包含枚举渲染逻辑
	// 实际实现时可从元数据中提取枚举信息
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

// renderRelation 渲染关系字段 (临时实现，需根据实际结构调整)
func (my *Renderer) renderRelation(buf *bytes.Buffer, className string, class *internal.Class) error {
	// TODO: 根据实际的关系信息实现
	// 此处仅为示例，项目中可能通过不同方式存储关系

	// 示例: 如果项目中关系信息存储在元数据的其他位置
	// 这里先暂时留空，避免错误

	return nil
}

// renderInput 渲染输入类型
func (my *Renderer) renderInput(buf *bytes.Buffer) error {
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

		// 定义输入类型
		fmt.Fprintf(buf, "input %sInput {\n", className)

		// 添加可输入字段，确保只处理真正的字段名
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			// 移除字段过滤逻辑，接受所有字段
			typeName := my.mapDBTypeToGraphQL(field.Type)

			// 输入类型所有字段都是可选的
			fmt.Fprintf(buf, "  %s: %s\n", fieldName, typeName)
		}

		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderFilter 渲染过滤器类型
func (my *Renderer) renderFilter(buf *bytes.Buffer) error {
	// 定义过滤器类型配置，每种类型支持的操作符
	filterTypes := map[string][]string{
		SCALAR_STRING:    {EQ, NE, LIKE, IN, NOT_IN},
		SCALAR_INT:       {EQ, NE, GT, GE, LT, LE, IN, NOT_IN},
		SCALAR_FLOAT:     {EQ, NE, GT, GE, LT, LE, IN, NOT_IN},
		SCALAR_BOOLEAN:   {EQ},
		SCALAR_DATE_TIME: {EQ, NE, GT, GE, LT, LE},
	}

	// 使用配置生成所有过滤器类型
	for scalarType, ops := range filterTypes {
		// 直接使用GraphQL标量类型名称作为过滤器名称前缀
		fmt.Fprintf(buf, "input %sFilter {\n", scalarType)

		for _, op := range ops {
			// 特殊处理需要数组类型的操作符
			if op == IN || op == NOT_IN {
				fmt.Fprintf(buf, "  %s: [%s!]\n", op, scalarType)
			} else {
				fmt.Fprintf(buf, "  %s: %s\n", op, scalarType)
			}
		}

		fmt.Fprintf(buf, "}\n\n")
	}

	// 为每个实体类型创建过滤器
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

		fmt.Fprintf(buf, "input %sFilter {\n", className)

		// 添加逻辑操作符
		logicOps := map[string]string{
			AND: "[%sFilter!]",
			OR:  "[%sFilter!]",
			NOT: "%sFilter",
		}

		for op, format := range logicOps {
			fmt.Fprintf(buf, "  %s: "+format+"\n", op, className)
		}

		// 为每个字段添加对应过滤器，确保只处理真正的字段名
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			filterType := my.mapDBTypeToGraphQL(field.Type) + "Filter"
			fmt.Fprintf(buf, "  %s: %s\n", fieldName, filterType)
		}

		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderOrder 渲染排序类型
func (my *Renderer) renderOrder(buf *bytes.Buffer) error {
	// 定义排序方向枚举
	fmt.Fprintf(buf, "enum OrderDirection {\n")
	fmt.Fprintf(buf, "  ASC\n")
	fmt.Fprintf(buf, "  DESC\n")
	fmt.Fprintf(buf, "}\n\n")

	// 为每个实体类型创建排序输入类型
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

		fmt.Fprintf(buf, "input %sOrder {\n", className)

		// 为每个可排序字段添加排序选项，确保只处理真正的字段名
		for fieldName, field := range class.Fields {
			// 确保只处理真正的字段名，跳过列名索引
			if fieldName != field.Name {
				continue
			}

			fmt.Fprintf(buf, "  %s: OrderDirection\n", fieldName)
		}

		fmt.Fprintf(buf, "}\n\n")
	}

	return nil
}

// renderQuery 渲染查询根类型
func (my *Renderer) renderQuery(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "type Query {\n")

	// 为每个实体类型生成查询字段
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

		// 单个实体查询
		fmt.Fprintf(buf, "  %s(id: ID!): %s\n",
			strcase.ToLowerCamel(className), className)

		// 列表查询
		fmt.Fprintf(buf, "  %sList(filter: %sFilter, order: %sOrder, limit: Int, offset: Int): [%s!]!\n",
			strcase.ToLowerCamel(className), className, className, className)

		// 计数查询
		fmt.Fprintf(buf, "  %sCount(filter: %sFilter): Int!\n",
			strcase.ToLowerCamel(className), className)
	}

	fmt.Fprintf(buf, "}\n\n")
	return nil
}

// renderMutation 渲染变更根类型
func (my *Renderer) renderMutation(buf *bytes.Buffer) error {
	fmt.Fprintf(buf, "type Mutation {\n")

	// 为每个实体类型生成变更字段
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

		// 创建操作
		fmt.Fprintf(buf, "  create%s(input: %sInput!): %s!\n",
			className, className, className)

		// 更新操作
		fmt.Fprintf(buf, "  update%s(id: ID!, input: %sInput!): %s!\n",
			className, className, className)

		// 删除操作
		fmt.Fprintf(buf, "  delete%s(id: ID!): Boolean!\n",
			className)
	}

	fmt.Fprintf(buf, "}\n\n")
	return nil
}

// 辅助方法：将数据库类型映射到GraphQL类型
func (my *Renderer) mapDBTypeToGraphQL(dbType string) string {
	// 转换为小写以进行大小写不敏感的匹配
	lowerDbType := strings.ToLower(dbType)

	// 使用Metadata中的TypeMapping，它已经在初始化时合并了dataTypes作为默认值
	if my.meta != nil && my.meta.cfg != nil && my.meta.cfg.Schema.TypeMapping != nil {
		if graphqlType, exists := my.meta.cfg.Schema.TypeMapping[lowerDbType]; exists {
			return graphqlType
		}
	}

	// 默认返回String类型，并记录警告
	log.Warn().Str("type", dbType).Msg("未知数据库类型，默认使用String")
	return SCALAR_STRING
}
