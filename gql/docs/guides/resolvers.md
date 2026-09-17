# 指南：Resolver 和 Codec

数据库 CRUD 自动生成。自定义计算、批量加载、本地或远程业务都注册同一个 `Resolver`。

## 普通字段

```go
builder.Register(gql.NewResolver("User", "greeting", "问候",
    func(ctx context.Context, source gql.Source, input struct{}) (string, error) {
        return "Hello, " + fmt.Sprint(source["name"]), nil
    }, gql.Requires("name")))
```

`Requires` 声明数据库字段依赖，由引擎补投影并剥除未选择字段。根字段使用 `Query` 或 `Mutation` 和 `gql.Root`；生产根字段还需用 `Access` 声明实际资源/动作。

## 批量计算

```go
builder.Register(gql.NewBatch("User", "reputation", "信誉",
    func(ctx context.Context, sources []gql.Source, input struct{}) ([]string, error) {
        return loadReputations(ctx, sources)
    }, gql.Requires("id")))
```

结果必须与 sources 等长对位。一次处理整批宿主，避免逐行查询。

## 按键加载

```go
builder.Register(gql.NewLoader("User", "profile", "画像", "id",
    func(ctx context.Context, keys []any) (map[any]Profile, error) {
        return service.BatchGet(ctx, keys)
    }))
```

这是一个批量 Resolver 的构造方式：引擎投影原始数据库键、去重取值并按宿主顺序回填；缺失键返回 null。数据来源可以是本地或远程。独立加载器并发取数，结果统一按 GraphQL Schema 完成，遵守租户与权限边界。`MaxBatch` 对按键加载限制去重键数，对普通批量计算限制宿主数。

## 覆盖和中间件

`builder.Replace(resolver)` 显式覆盖已有字段，无需额外声明。`builder.Wrap(coordinate, middleware)` 装饰已注册 Resolver；普通 Register 不会静默覆盖。

普通中间件使用 `NextResolver[Source, Args, Value]`；批量中间件使用 `NextResolver[[]Source, Args, []Value]`，next 一次处理整个批次。中间件保持字段声明、权限和批量能力。

Mutation 业务通过 `ExecutionContextFromContext(ctx)` 的 `Transaction()` 获取当前 `*sql.Tx`，检查非 nil 后执行 SQL。Query 有当前事务时也必须使用它；只有没有事务时才使用 `Connection().Pool()`。事务由引擎提交或回滚。远程调用的外部副作用不加入本地数据库事务。

## Codec

`WithCodecs` 注册 ID、日期或自定义标量的输入/输出转换。按键加载的内部键保留数据库原值；没有自定义字段的查询保持 JSON 字节直通。
