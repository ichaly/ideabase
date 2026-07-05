// gql引擎完整演示：数据库自动映射GraphQL + 自定义resolver + 持久化查询 + CDC订阅
// 运行步骤见 README.md：docker compose up -d 后 go run .
package main

import (
	"cmp"
	"context"
	"fmt"
	"os"

	"github.com/gofiber/fiber/v3"
	"github.com/ichaly/ideabase/gql"
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/ichaly/ideabase/std"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
	return cmp.Or(os.Getenv("DEMO_DSN"), "host=localhost port=5678 user=postgres password=postgres dbname=demo sslmode=disable")
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
	k.Set("metadata.classes", map[string]*gql.ClassConfig{
		"User": {Table: "users", Search: []string{"name"}}, // 声明搜索列后获得 search 参数
		"Post": {Table: "posts", Search: []string{"title", "content"}},
	})

	meta, err := gql.NewMetadata(k, db)
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
	// 注册自定义resolver：字段挂载与schema由引擎反射签名完成
	if err = executor.Register(signResolver()); err != nil {
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
	executor.Bind(app.Group(executor.Path()))

	fmt.Println("GraphQL服务: http://localhost:8080/graphql （示例见 example/README.md）")
	die(app.Listen(":8080"))
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}
