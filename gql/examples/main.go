// gql引擎完整演示：数据库自动映射GraphQL + 自定义resolver + 持久化查询 + CDC订阅
// 运行步骤见 README.md：docker compose up -d 后 go run .
package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ichaly/ideabase/gql"
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// UpperCodec 展示业务自定义标量、Matcher主动认领字段和Baser复用String过滤器。
type UpperCodec struct{}

func (UpperCodec) Name() string { return "Upper" }
func (UpperCodec) Base() string { return protocol.SCALAR_STRING }
func (UpperCodec) Match(_ *protocol.Class, field *protocol.Field) bool {
	return field.Column == "name"
}
func (UpperCodec) Encode(token []byte) []byte {
	value, err := strconv.Unquote(string(token))
	if err != nil {
		return nil
	}
	return []byte(strconv.Quote(strings.ToUpper(value)))
}
func (UpperCodec) Decode(value any) any {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return value
}

type helloArgs struct {
	Lang string `json:"lang" validate:"omitempty,oneof=zh en" doc:"语言"`
}

type echoArgs struct {
	Message string `json:"message" validate:"required" doc:"原样返回的消息"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Reputation struct {
	Level int    `json:"level"`
	Title string `json:"title"`
}

func greetingResolver() gql.Resolver {
	return gql.NewResolver("User", "greeting", "个性化问候",
		func(_ context.Context, source gql.Source, args helloArgs) (string, error) {
			name := fmt.Sprint(source["name"])
			if args.Lang == "en" {
				return "Hello, " + name, nil
			}
			return "你好，" + name, nil
		})
}

func serverInfoResolver() gql.Resolver {
	return gql.NewResolver("Query", "serverInfo", "服务信息",
		func(context.Context, gql.Root, struct{}) (ServerInfo, error) {
			return ServerInfo{Name: "gql-complete-demo", Version: "1.0"}, nil
		}, gql.Extra("type ServerInfo { name: String! version: String! }"))
}

func echoResolver() gql.Resolver {
	return gql.NewResolver("Mutation", "echo", "自定义突变",
		func(_ context.Context, _ gql.Root, args echoArgs) (string, error) { return args.Message, nil })
}

func reputationRemote() gql.Remote {
	return gql.NewRemote("User", "reputation", "远程声望", "id",
		func(_ context.Context, keys []any) (map[any]Reputation, error) {
			out := make(map[any]Reputation, len(keys))
			for _, key := range keys {
				out[key] = Reputation{Level: 7, Title: "数据库达人"}
			}
			return out, nil
		})
}

// signResolver 自定义字段解析器示例（注册即声明）：为用户生成署名。
// NewBatch整页一次调用免N+1；source是该行已查出的字段——resolver只能读到
// 查询选择了的字段（引擎不会偷偷多查），依赖email时查询需一并选择
func signResolver() gql.Resolver {
	return gql.NewBatch("User", "sign", "署名",
		func(_ context.Context, sources []gql.Source, _ struct{}) ([]string, error) {
			values := make([]string, len(sources))
			for i, source := range sources {
				if email, ok := source["email"]; ok {
					values[i] = fmt.Sprintf("%v <%v>", source["name"], email)
				} else {
					values[i] = fmt.Sprint(source["name"])
				}
			}
			return values, nil
		})
}

// demoDSN demo数据库连接串（docker compose暴露5433），可用DEMO_DSN覆盖
func demoDSN() string {
	return cmp.Or(os.Getenv("DEMO_DSN"), "host=localhost port=5433 user=demo password=demo dbname=demo sslmode=disable")
}

// buildExecutor 装配完整引擎：配置 -> 元数据 -> 编译器（方言自注册） -> 执行器 + resolver
// main与性能测试共用，保证基准跑的就是demo实际装配
func buildExecutor(db *gorm.DB) (*gql.Executor, error) {
	// 配置：数据侧声明（搜索列等）——生产中可放config.yml，键名一致；
	// resolver等行为侧声明走注册（NewResolver/NewBatch），不进配置
	k, err := std.NewKonfig()
	if err != nil {
		return nil, err
	}
	k.Set("mode", "dev")
	k.Set("app.root", ".") // schema.graphql与元数据缓存输出到 ./cfg
	k.Set("metadata.include-tables", []string{
		"users", "posts", "tags", "post_tags", "comments",
		"uuid_records", "snowflake_records", "virtual_records",
	})
	k.Set("metadata.classes", map[string]any{
		"User": map[string]any{"table": "users", "search": []string{"name"}},
		"Post": map[string]any{
			"table":  "posts",
			"search": []string{"title", "content"},
			"scope":  []map[string]string{{"column": "tenant_id", "context": "tenant"}},
		},
		"UuidRecord":      map[string]any{"table": "uuid_records", "id-generator": "database"},
		"SnowflakeRecord": map[string]any{"table": "snowflake_records", "id-generator": "snowflake"},
		"VirtualRecord":   map[string]any{"table": "virtual_records", "id-generator": "virtual"},
	})

	meta, err := gql.NewMetadata(k, db,
		gql.WithCodecs(gql.NewIdCodec(), UpperCodec{}),
		gql.WithIDGenerator("virtual", func() (any, error) {
			return "v_" + strconv.FormatInt(time.Now().UnixNano(), 36), nil
		}),
	)
	if err != nil {
		return nil, err
	}
	compiler, err := gql.NewCompiler(meta, nil)
	if err != nil {
		return nil, err
	}
	executor, err := gql.NewExecutor(db, gql.NewRenderer(meta), meta, compiler)
	if err != nil {
		return nil, err
	}
	// 普通/批量/根字段Resolver与远程关系使用同一个注册入口。
	if err = executor.Register(signResolver(), greetingResolver(), serverInfoResolver(), echoResolver(), reputationRemote()); err != nil {
		return nil, err
	}
	// 中间件按坐标增强已注册Resolver，不重写业务函数。
	if err = executor.Wrap("User.greeting", gql.NewResolverMiddleware(
		func(ctx context.Context, source gql.Source, args helloArgs,
			next gql.NextResolver[gql.Source, helloArgs, string],
		) (string, error) {
			value, err := next(ctx, source, args)
			return "[wrapped] " + value, err
		})); err != nil {
		return nil, err
	}
	return executor, nil
}

func main() {
	// 1. 数据库连接 + 引擎装配
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	die(err)
	executor, err := buildExecutor(db)
	die(err)

	// 2. 加载持久化查询文档
	die(executor.LoadDocuments("./queries"))

	// 3. HTTP服务：POST /graphql 查询变更，GET /graphql 订阅WebSocket升级
	app := fiber.New()
	// 演示服务端作用域注入。生产应从认证声明读取，不能信任客户端Header。
	app.Use(func(c fiber.Ctx) error {
		tenant, _ := strconv.Atoi(strings.TrimSpace(c.Get("X-Demo-Tenant")))
		if tenant == 0 {
			tenant = 1
		}
		c.SetContext(gql.WithScope(c.Context(), map[string]any{"tenant": tenant}))
		return c.Next()
	})
	executor.Bind(app.Group(executor.Path()))

	fmt.Println("GraphQL服务: http://localhost:8080/graphql （示例见 examples/README.md）")
	die(app.Listen(":8080"))
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}
