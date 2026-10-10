# 指南：Resolver 和 Codec

数据库 CRUD 自动生成。自定义计算、批量加载、本地或远程业务都注册同一个 `Resolver`。

## 普通字段

```go
builder.Register(gql.NewResolver("User", "greeting", "问候",
    func(ctx context.Context, source gql.Source, input struct{}) (string, error) {
        return "Hello, " + fmt.Sprint(source["name"]), nil
    }, gql.Requires("name")))
```

`Requires` 声明数据库字段依赖，由引擎补投影并剥除未选择字段。根字段使用 `Query` 或 `Mutation` 和 `gql.Root[I, O]`；新增的生产根字段还需用 `Access` 声明实际资源/动作。

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

## 新增、覆盖与扩展

所有注册都使用 `builder.Register(gql.NewResolver(...))`。已有字段沿用原 schema，新字段从函数签名生成 schema。
根 Resolver 使用 `Root[I, O]`，I 为完整参数，O 为结果；两者必须与回调签名一致。
调用 `root.Next(args)` 继续当前字段的原生实现，不调用则自行返回结果。Next 沿用当前请求上下文，
只允许调用一次；没有原生实现、重复调用或回调结束后调用都明确失败。

create/update 各自注册；复用同一个 Go 函数或输入结构体即可共享处理。`Has("input.name")` 保留 PATCH 的字段存在性，
`Include("input.name")` 加入省略字段。update 仅校验选定字段；批量输入切片用 `validate:"dive"` 校验元素。
返回结构体中声明的字段由原生 SQL 补投影，仍受权限约束；nullable 单条结果使用指针，先判断 nil 再写审计。

删除 Replace、Wrap 以及专用 Resolver 中间件入口。实体字段的公共处理直接复用业务函数；原生根字段通过 Next 扩展。

Mutation 业务通过 `ExecutionFrom(ctx)` 的 `Transaction()` 获取当前 `*sql.Tx`，检查非 nil 后执行 SQL。Query 有当前事务时也必须使用它；只有没有事务时才使用 `Connection().Pool()`。事务由引擎提交或回滚。远程调用的外部副作用不加入本地数据库事务。

## Codec

`WithCodecs` 注册 ID、日期或自定义标量的输入/输出转换。按键加载的内部键保留数据库原值；没有自定义字段的查询保持 JSON 字节直通。
