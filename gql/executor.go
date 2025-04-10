package gql

import (
	"context"
	"fmt"
	"strings"

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
	gqlRequest struct {
		Query         string                 `json:"query"`
		OperationName string                 `json:"operationName"`
		Variables     map[string]interface{} `json:"variables"`
	}
	gqlResult struct {
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
func (my *Executor) compile(operation *ast.OperationDefinition, variables RawMessage) (string, []interface{}) {
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
	var req gqlRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
		})
	}

	// 将变量转换为RawMessage
	var variables RawMessage
	if len(req.Variables) > 0 {
		var err error
		variables, err = json.Marshal(req.Variables)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
			})
		}
	}

	// 执行GraphQL查询
	result := my.Execute(c.Context(), req.Query, variables)

	// 返回结果
	return c.JSON(result)
}

// Execute 执行GraphQL查询
func (my *Executor) Execute(ctx context.Context, query string, variables RawMessage) gqlResult {
	var r gqlResult

	// 解析变量
	var vars map[string]interface{}
	if len(variables) > 0 {
		if err := json.Unmarshal(variables, &vars); err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return r
		}
	}

	// 处理自省查询
	if isIntrospectionQuery(query) {
		// 处理自省查询
		data, err := my.intro.Introspect(ctx, query, vars)
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

	// 执行所有操作
	r = my.executeOperations(doc.Operations, variables)
	return r
}

// executeOperations 执行给定的操作列表（支持并发）
func (my *Executor) executeOperations(ops []*ast.OperationDefinition, variables RawMessage) gqlResult {
	var r gqlResult
	r.Data = make(map[string]interface{})

	// 创建结果通道
	type opResult struct {
		name string
		data map[string]interface{}
		err  error
	}
	results := make(chan opResult, len(ops))

	// 并行执行所有操作
	for _, op := range ops {
		go func(operation *ast.OperationDefinition) {
			// 编译并执行
			sql, args := my.compile(operation, variables)
			data := make(map[string]interface{})
			err := my.db.Raw(sql, args...).Scan(&data).Error

			// 发送结果
			results <- opResult{
				name: operation.Name,
				data: data,
				err:  err,
			}
		}(op)
	}

	// 收集结果
	for range ops {
		result := <-results

		// 处理错误
		if result.err != nil {
			r.Errors = append(r.Errors, gqlerror.Wrap(result.err))
		}

		// 按GraphQL规范组织结果
		if result.name != "" {
			// 命名操作使用操作名作为键
			r.Data[result.name] = result.data
		} else {
			// 未命名操作直接合并到顶层
			for k, v := range result.data {
				r.Data[k] = v
			}
		}
	}

	return r
}

// isIntrospectionQuery 判断是否是自省查询
func isIntrospectionQuery(query string) bool {
	// 检查是否包含__schema或__type查询
	return strings.Contains(query, "__schema") || strings.Contains(query, "__type")
}
