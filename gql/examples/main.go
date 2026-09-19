// gql引擎完整演示：数据库自动映射GraphQL + 自定义resolver + 持久化查询 + CDC订阅
// 运行步骤见 README.md：docker compose up -d 后 go run .
package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ichaly/ideabase/gql"
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/gql/transport"
	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
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

type demoDenyKey struct{}

// demoDelegationVerifier is intentionally deterministic and local-only. It
// makes the control-plane endpoint executable in this self-contained demo;
// production deployments must use DelegationCodec (or an equivalent signer).
type demoDelegationVerifier struct{}

func (demoDelegationVerifier) Verify(ctx context.Context, token, _ string) (context.Context, error) {
	if token != "demo-token" {
		return nil, fmt.Errorf("invalid demo delegation")
	}
	return demoContext(ctx, 1), nil
}

func greetingResolver() gql.Resolver {
	return gql.NewResolver("User", "greeting", "个性化问候",
		func(_ context.Context, source gql.Source, args helloArgs) (string, error) {
			name := fmt.Sprint(source["name"])
			if args.Lang == "en" {
				return "Hello, " + name, nil
			}
			return "你好，" + name, nil
		}, gql.Access("users", "read"))
}

func serverInfoResolver() gql.Resolver {
	return gql.NewResolver("Query", "serverInfo", "服务信息",
		func(context.Context, gql.Root, struct{}) (ServerInfo, error) {
			return ServerInfo{Name: "gql-complete-demo", Version: "1.0"}, nil
		}, gql.Extra("type ServerInfo { name: String! version: String! }"), gql.Access("system", "read"))
}

func echoResolver() gql.Resolver {
	return gql.NewResolver("Mutation", "echo", "自定义突变",
		func(_ context.Context, _ gql.Root, args echoArgs) (string, error) { return args.Message, nil }, gql.Access("system", "write"))
}

func reputationResolver() gql.Resolver {
	return gql.NewLoader("User", "reputation", "远程声望", "id",
		func(_ context.Context, keys []any) (map[any]Reputation, error) {
			out := make(map[any]Reputation, len(keys))
			for _, key := range keys {
				out[key] = Reputation{Level: 7, Title: "数据库达人"}
			}
			return out, nil
		}, gql.Access("reputation", "read"))
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
		}, gql.Access("users", "read"))
}

// demoDSN demo数据库连接串（docker compose暴露5433），可用DEMO_DSN覆盖
func demoDSN() string {
	return cmp.Or(os.Getenv("DEMO_DSN"), "postgres://postgres:postgres@localhost:5678/demo?sslmode=disable")
}

// buildEngine 用统一入口装配完整引擎；Builder 是元数据、编译器与运行时的唯一组装点。
// main与性能测试共用，保证基准跑的就是demo实际装配
func buildEngine(db *std.Connection) (*gql.Engine, error) {
	return buildEngineConfigured(db, false, nil)
}

func buildEngineMode(db *std.Connection, production bool) (*gql.Engine, error) {
	return buildEngineConfigured(db, production, nil)
}

func buildEngineConfigured(db *std.Connection, production bool, observer gql.Observer) (*gql.Engine, error) {
	// 配置：数据侧声明（搜索列等）——生产中可放config.yml，键名一致；
	// resolver等行为侧声明走注册（NewResolver/NewBatch），不进配置
	k, err := std.NewKonfig()
	if err != nil {
		return nil, err
	}
	k.Set("mode", "dev")
	k.Set("schema.introspection", true)
	k.Set("app.root", ".") // schema.graphql与元数据缓存输出到 ./cfg
	if production {
		k.Set("schema.introspection", false)
		k.Set("schema.persisted-only", true)
		k.Set("schema.default-limit", 50)
		k.Set("schema.max-depth", 12)
		k.Set("schema.max-cost", 100000)
		k.Set("subscription.publication", "ideabase_cdc")
	}
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

	builder := gql.New(k, db,
		gql.WithCodecs(gql.NewIdCodec(), UpperCodec{}),
		gql.WithIDGenerator("virtual", func() (any, error) {
			return "v_" + strconv.FormatInt(time.Now().UnixNano(), 36), nil
		}),
	)
	if err = builder.Observe(observer); err != nil {
		return nil, err
	}
	if err = builder.Policy(gql.OperationPolicyProvider(func(ctx context.Context, _ gql.PolicyRequest) (gql.PolicyDecision, error) {
		if denied, _ := ctx.Value(demoDenyKey{}).(bool); denied {
			return gql.PolicyDecision{Allow: false}, nil
		}
		fields := make(map[gql.FieldUse][]string)
		for _, use := range []gql.FieldUse{
			gql.FieldUseSelect, gql.FieldUseFilter, gql.FieldUseOrder, gql.FieldUseGroup,
			gql.FieldUseAggregate, gql.FieldUseJoin, gql.FieldUseWrite, gql.FieldUseReturn,
			gql.FieldUseInternalDependency,
		} {
			fields[use] = []string{"*"}
		}
		return gql.PolicyDecision{Allow: true, Fields: fields}, nil
	})); err != nil {
		return nil, err
	}
	// 普通/批量/根字段Resolver与远程关系使用同一个注册入口。
	if err = builder.Register(signResolver(), greetingResolver(), serverInfoResolver(), echoResolver(), reputationResolver()); err != nil {
		return nil, err
	}
	// 中间件按坐标增强已注册Resolver，不重写业务函数。
	if err = builder.Wrap("User.greeting", gql.NewResolverMiddleware(
		func(ctx context.Context, source gql.Source, args helloArgs,
			next gql.NextResolver[gql.Source, helloArgs, string],
		) (string, error) {
			value, err := next(ctx, source, args)
			return "[wrapped] " + value, err
		})); err != nil {
		return nil, err
	}
	documentDirs := []string{"./queries", "./operations/public"}
	if !production {
		documentDirs = append(documentDirs, "./operations/dev")
	}
	if err = builder.LoadDocuments(documentDirs...); err != nil {
		return nil, err
	}
	engine, err := builder.Build()
	if err != nil {
		return nil, err
	}
	return engine, nil
}

func demoContext(ctx context.Context, tenant int) context.Context {
	identity, err := gql.NewIdentity(gql.IdentityInput{
		Tenant: strconv.Itoa(tenant), Subject: "demo-user", Roles: []string{"demo"},
	})
	if err != nil {
		panic(err) // fixed demo values always satisfy the identity contract
	}
	return gql.WithScope(gql.WithIdentity(ctx, identity), map[string]any{"tenant": tenant})
}

func main() {
	// 1. 数据库连接 + 引擎装配
	db, err := openDemoDB()
	die(err)
	defer db.Close()
	engine, err := buildEngine(db)
	die(err)

	// 2. HTTP服务：标准 net/http 查询与 SSE 订阅；WebSocket 不在示例中提供。
	addr := cmp.Or(os.Getenv("DEMO_HTTP_ADDR"), ":8080")
	fmt.Printf("GraphQL服务: http://localhost%s/graphql （示例见 examples/README.md）\n", addr)
	die(http.ListenAndServe(addr, newDemoHandler(engine)))
}

func openDemoDB() (*std.Connection, error) {
	config := &std.Config{}
	config.Database = demoDSN()
	db, err := std.NewConnection(config)
	if err != nil {
		return nil, fmt.Errorf("demo数据库不可用（先运行 docker compose up -d）: %w", err)
	}
	if err = db.Pool().Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("demo数据库无法连接（先运行 docker compose up -d）: %w", err)
	}
	return db, nil
}

// newDemoHandler 是真实 Demo 服务与 HTTP 验收测试共用的生产入口。
func newDemoHandler(engine *gql.Engine) http.Handler {
	mux := http.NewServeMux()
	router, err := gql.NewTenantRouter(gql.TenantRouterConfig{Mode: gql.TenantModeDatabase, Revision: 1, Routes: map[string]gql.TenantRoute{
		"1": {DataSource: "demo"}, "2": {DataSource: "demo"},
	}})
	if err != nil {
		panic(fmt.Sprintf("build demo tenant router: %v", err))
	}
	security := gql.NewRevocationBarrier(1, map[string]gql.TenantSecurityState{
		"1": {MinimumRouteRevision: 1}, "2": {MinimumRouteRevision: 1},
	})
	tenantGateway, err := gql.NewTenantGateway(router, map[string]*gql.Engine{"demo": engine}, gql.WithSecurityState(security))
	if err != nil {
		panic(fmt.Sprintf("build demo tenant gateway: %v", err))
	}
	controlAuth := func(r *http.Request) bool { return r.Header.Get("X-Demo-Control") == "demo" }
	mux.Handle("/health", transport.NewHealthHandler(tenantGateway))
	mux.Handle("/manifest", transport.NewManifestHandler("demo", tenantGateway, controlAuth))
	mux.Handle("/effective-schema", transport.NewEffectiveSchemaHandler(tenantGateway, demoDelegationVerifier{}))
	mux.Handle("/.well-known/gql-gateway", transport.NewTransportDescriptorHandler(transport.TransportDescriptor{
		EndpointID: "demo", ManifestURL: "/manifest", EffectiveSchemaURL: "/effective-schema", ExecuteURL: "/graphql", SubscribeURL: "/graphql", HealthURL: "/health",
	}))
	// 演示服务端作用域注入。生产应从认证声明读取，不能信任客户端Header。
	withDemoContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant, _ := strconv.Atoi(strings.TrimSpace(r.Header.Get("X-Demo-Tenant")))
			if tenant == 0 {
				tenant = 1
			}
			ctx := demoContext(r.Context(), tenant)
			if r.Header.Get("X-Demo-Deny") == "true" {
				ctx = context.WithValue(ctx, demoDenyKey{}, true)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	mux.Handle("/graphql", withDemoContext(transport.NewHandler(tenantGateway, transport.Options{})))
	return mux
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}
