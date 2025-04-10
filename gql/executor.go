package gql

import (
	"context"
	"fmt"
	"strings"

	"github.com/duke-git/lancet/v2/strutil"
	"github.com/gofiber/fiber/v2"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// 方言注册表
var dialects = make(map[string]Dialect)

// RegisterDialect 全局注册方法，用于各个方言包在init函数中调用
func RegisterDialect(name string, dialect Dialect) {
	dialects[name] = dialect
}

// 请求和结果类型定义
type (
	gqlQuery struct {
		Query         string                 `json:"query"`
		OperationName string                 `json:"operationName"`
		Variables     map[string]interface{} `json:"variables"`
	}
	gqlReply struct {
		sql    string
		args   []any
		Data   map[string]interface{} `json:"data,omitempty"`
		Errors gqlerror.List          `json:"errors,omitempty"`
	}
)

// Executor GraphQL执行器
type Executor struct {
	db      *gorm.DB
	meta    *Metadata
	intro   *intro.Handler
	schema  *ast.Schema
	dialect Dialect
}

// NewExecutor 创建一个新的执行器
func NewExecutor(d *gorm.DB, r *Renderer, m *Metadata) (*Executor, error) {
	executor := &Executor{
		db:   d,
		meta: m,
	}

	// 选择SQL方言
	if err := executor.selectDialect(); err != nil {
		return nil, err
	}

	// 生成并加载GraphQL模式
	data, err := r.Generate()
	if err != nil {
		return nil, err
	}
	s, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema.graphql",
		Input: string(data),
	})
	if err != nil {
		return nil, err
	}

	executor.schema = s
	executor.intro = intro.New(s)
	return executor, nil
}

// selectDialect 选择适合的SQL方言
func (my *Executor) selectDialect() error {
	// 1. 首先尝试根据数据库类型选择方言
	if my.meta != nil && my.meta.db != nil {
		dbName := my.meta.db.Name()

		// 根据数据库驱动名称匹配方言
		switch {
		case strings.Contains(dbName, "postgres"):
			if dialect, ok := dialects["postgresql"]; ok {
				my.dialect = dialect
			}
		case strings.Contains(dbName, "mysql"):
			if dialect, ok := dialects["mysql"]; ok {
				my.dialect = dialect
			}
		}
	}

	// 2. 如果未找到匹配方言，尝试使用PostgreSQL方言（如果存在）
	if my.dialect == nil && len(dialects) > 0 {
		if dialect, ok := dialects["postgresql"]; ok {
			my.dialect = dialect
		} else {
			// 3. 否则使用第一个可用的方言
			for _, dialect := range dialects {
				my.dialect = dialect
				break
			}
		}
	}

	// 4. 如果仍未找到方言，返回错误
	if my.dialect == nil {
		return fmt.Errorf("没有可用的SQL方言实现，请确保导入了相应的dialect包")
	}

	return nil
}

// compile 编译GraphQL操作为SQL (内部方法)
func (my *Executor) compile(operation *ast.OperationDefinition, variables map[string]interface{}) (string, []interface{}) {
	cpl := NewCompiler(my.meta)
	defer cpl.Release()      // 使用完毕后释放回对象池
	cpl.dialect = my.dialect // 设置共享的方言实现
	cpl.Build(operation, variables)
	return cpl.String(), cpl.Args()
}

// Base 实现Plugin接口的Base方法，返回插件的基础路径
func (my *Executor) Base() string {
	return "/graphql"
}

// Init 实现Plugin接口的Init方法，初始化插件
func (my *Executor) Init(r fiber.Router) {
	// 注册GraphQL请求处理路由
	r.Post("/", my.Handler)
}

// Handler 处理GraphQL请求
func (my *Executor) Handler(c *fiber.Ctx) error {
	// 解析请求
	var req gqlQuery
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
		})
	}

	// 直接使用map类型的变量
	result := my.Execute(c.Context(), req.Query, req.Variables, req.OperationName)

	// 返回结果
	return c.JSON(result)
}

// Execute 执行GraphQL查询
func (my *Executor) Execute(ctx context.Context, query string, variables map[string]interface{}, operationName string) gqlReply {
	var r gqlReply

	// 处理自省查询
	if strutil.ContainsAny(query, []string{"__schema", "__type"}) {
		data, err := my.intro.Introspect(ctx, query, variables)
		if err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return r
		}

		r.Data = data
		return r
	}

	// 解析查询
	doc, err := gqlparser.LoadQuery(my.schema, query)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}

	// 按照GraphQL规范处理操作
	operation, opErr := getOperation(doc.Operations, operationName)
	if opErr != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(opErr)}
		return r
	}

	// 执行选定的操作
	r = my.runOperation(operation, variables)
	return r
}

// getOperation 根据GraphQL标准确定要执行的操作
func getOperation(operations ast.OperationList, operationName string) (*ast.OperationDefinition, error) {
	// 单操作直接返回，多操作需要操作名
	if len(operations) == 1 {
		return operations[0], nil
	} else if operationName == "" {
		return nil, fmt.Errorf("必须提供operationName，因为该查询包含多个操作")
	}

	// 查找指定操作
	for _, op := range operations {
		if op.Name == operationName {
			return op, nil
		}
	}
	return nil, fmt.Errorf("未找到名为'%s'的操作", operationName)
}

// runOperation 执行单个GraphQL操作
func (my *Executor) runOperation(op *ast.OperationDefinition, variables map[string]interface{}) gqlReply {
	var r gqlReply

	// 编译并执行SQL查询
	r.sql, r.args = my.compile(op, variables)

	result := make(map[string]interface{})
	if err := my.db.Raw(r.sql, r.args...).Scan(&result).Error; err != nil {
		r.Errors = append(r.Errors, gqlerror.Wrap(err))
		return r
	}

	// 按GraphQL规范组织结果
	if op.Name != "" {
		r.Data = map[string]interface{}{op.Name: result}
	} else {
		r.Data = result
	}

	return r
}
