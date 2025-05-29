# IdeaBase 元数据加载模块

## 设计思想

元数据加载系统是 IdeaBase 的核心基础设施，旨在为 GraphQL 查询编译和 SQL 生成提供统一、灵活、可扩展的数据库结构描述。其设计目标包括：

- **多源融合**：支持数据库、文件、配置三类数据源，自动合并并按优先级覆盖。
- **环境自适应**：开发环境优先数据库直连，生产环境优先文件缓存，配置始终可增强和覆盖。
- **高可扩展性**：采用策略模式，Loader 可插拔，优先级可控，便于扩展新数据源。
- **一致性与性能**：多重索引、命名规范化、关系自动推导，保证运行时高效与一致。
- **易用性**：配置灵活，支持虚拟表、字段别名、关系自定义、字段过滤等高级特性。

## 实现架构

### Loader 策略模式

系统内置四种 Loader，均实现统一接口：

- **PgsqlLoader**：从 PostgreSQL 数据库实时加载表、字段、主外键、关系等元数据。
- **MysqlLoader**：从 MySQL 数据库实时加载结构信息。
- **FileLoader**：从 JSON 文件加载预生成的元数据快照，适合生产环境。
- **ConfigLoader**：从应用配置加载自定义元数据，支持虚拟表、字段、关系、别名等。

Loader 通过优先级排序，依次执行，后加载的可覆盖前者。Loader 支持动态增删、钩子扩展。

#### Loader 优先级（默认）

- 数据库（Pgsql/Mysql）：60
- 文件：80
- 配置：100

### 合并与覆盖策略

- **主流程**：
  1. 按优先级依次执行 Loader，将元数据注入 Hoster（即 Metadata 实例）。
  2. 同名类/字段后加载的覆盖前者，配置源拥有最高优先级。
  3. 虚拟表、虚拟字段只能通过配置源创建。
- **字段合成**：
  - 字段分组排序：主字段 > 标准字段 > 覆盖 > 别名 > 虚拟。
  - 别名字段依赖主字段，虚拟字段完全自定义。
  - 字段过滤（include/exclude）在字段合成后应用。

### 命名规范与多重索引

- **命名规范化**：
  - 支持下划线转驼峰、表名前缀去除、单复数自动转换。
  - 可通过配置自定义表名、字段名映射。
- **多重索引**：
  - 类名、表名、别名均可索引 Class。
  - 字段名、列名、别名均可索引 Field。
  - 支持通过表名、类名、别名、字段名、列名多路径查找。

### 关系自动推导

- 自动识别一对多、多对一、多对多、自关联等关系。
- 多对多关系自动识别中间表，支持通过配置自定义中间表结构。
- 关系字段自动生成，支持正反向导航。

### 并发与性能

- Metadata 实例线程安全，Loader 幂等。
- 文件加载和缓存机制优化生产环境性能。
- 数据库 Loader 仅在开发/调试模式下启用。

## 配置选项

元数据加载模块的配置分为两大部分：`schema` 和 `metadata`，分别对应 `SchemaConfig` 和 `MetadataConfig` 结构体。

### 1. schema（SchemaConfig）

| 字段名        | 类型              | 默认值 | 说明                           |
| ------------- | ----------------- | ------ | ------------------------------ |
| schema        | string            | public | 数据库 schema 名               |
| default-limit | int               | 10     | 默认分页限制                   |
| mapping       | map[string]string | 空     | 数据类型映射（如 int→integer） |

**示例：**

```yaml
schema:
  schema: public # 数据库schema名
  default-limit: 10 # 默认分页限制
  mapping: # 数据类型映射（可选）
    int: integer
    varchar: string
```

### 2. metadata（MetadataConfig）

| 字段名         | 类型                     | 默认值 | 说明                             |
| -------------- | ------------------------ | ------ | -------------------------------- |
| classes        | map[string]\*ClassConfig | 空     | 类定义映射（key 为类名）         |
| file           | string                   | 空     | 元数据文件路径，支持{mode}占位符 |
| use-camel      | bool                     | true   | 是否使用驼峰命名                 |
| use-singular   | bool                     | true   | 是否使用单数类名                 |
| show-through   | bool                     | true   | 是否显示多对多中间表             |
| table-prefix   | []string                 | 空     | 需要去除的表名前缀               |
| exclude-tables | []string                 | 空     | 需要排除的表名                   |
| exclude-fields | []string                 | 空     | 需要排除的字段名                 |

**示例：**

```yaml
metadata:
  file: cfg/metadata.{mode}.json # 元数据文件路径，支持{mode}占位符
  use-camel: true # 是否使用驼峰命名
  use-singular: true # 是否使用单数类名
  show-through: true # 是否显示多对多中间表
  table-prefix: [tbl_, app_] # 需要去除的表名前缀
  exclude-tables: [audit_log] # 排除的表名
  exclude-fields: [password] # 排除的字段名
  classes:
    User:
      table: users
      description: "用户信息"
      primary_keys: [id]
      resolver: "UserResolver"
      fields:
        id:
          column: id
          type: integer
          primary: true
        username:
          column: user_name
          type: string
        email:
          column: email
          type: string
      exclude_fields: [password]
      include_fields: [id, username, email]
      override: false
```

> 详细的 `ClassConfig`、`FieldConfig`、`RelationConfig`、`ThroughConfig` 字段说明请参考 internal/config.go 或相关文档。

## 典型用法

### 1. 自动环境切换

```go
meta, err := gql.NewMetadata(konfig, db) // dev: 数据库优先，prod: 文件优先
```

### 2. 自定义 Loader 组合

```go
meta, err := gql.NewMetadata(konfig, db,
    WithoutLoader(metadata.LoaderPgsql),
    WithLoader(NewCustomLoader(...)),
)
```

### 3. 配置虚拟表/字段/关系

```yaml
metadata:
  classes:
    Statistics:
      virtual: true
      description: "统计数据"
      fields:
        totalUsers:
          type: integer
          resolver: CountUsersResolver
```

### 4. 字段过滤与别名

```yaml
metadata:
  classes:
    PublicUser:
      table: users
      exclude_fields: ["password", "phone"]
      fields:
        email:
          description: "脱敏邮箱"
          resolver: MaskedEmailResolver
```

### 5. 多对多关系与中间表

```yaml
metadata:
  classes:
    Post:
      table: posts
      fields:
        tags:
          virtual: true
          relation:
            target_class: Tag
            type: many_to_many
            through:
              table: post_tags
              source_key: post_id
              target_key: tag_id
```

## 数据结构

- **主索引**：`Nodes` - 类名到类定义的映射（支持表名、别名多重索引）
- **Class/Field/Relation**：详见 internal 包定义
- **多重索引**：支持通过类名、表名、别名、字段名、列名多路径查找

## 主要接口与数据结构

- `Loader`：Name()、Load(Hoster)、Support()、Priority()
- `Hoster`：PutClass/GetClass/SetVersion
- `Metadata.Nodes`：多重索引，类名/表名/别名均可查
- `internal.Class`/`internal.Field`/`internal.Relation`：详见 internal 包

## 最佳实践

1. **开发环境**：优先数据库直连，调试结构变更，自动导出文件缓存。
2. **生产环境**：优先文件加载，禁用数据库直连，配置增强。
3. **字段过滤**：优先用 include_fields 精简视图，exclude_fields 排除敏感字段。
4. **关系配置**：复杂关系建议显式配置，避免自动推导误判。
5. **虚拟表/字段**：仅通过配置源定义，适合统计、聚合、外部数据等场景。
6. **索引一致性**：类名、表名、别名、字段名、列名均可查找，便于兼容多种访问方式。

## 注意事项

- 配置源定义的类/字段/关系拥有最高优先级，始终覆盖其他源。
- 虚拟表/字段不会自动出现在数据库或文件源中，需显式配置。
- 多对多关系自动识别仅限于典型中间表结构，复杂场景建议手动配置。
- 文件缓存建议定期导出，保持与数据库结构同步。
- Loader 支持自定义扩展，建议实现 Support() 以适配不同环境。

## 参考实现与扩展建议

- 可扩展更多 Loader（如 REST、gRPC、NoSQL 等）。
- 支持 Loader 生命周期钩子、健康检查、动态重载。
- 提供元数据变更监听与热更新能力。
- 支持多语言/多租户场景下的元数据隔离。

---

如需详细接口说明、数据结构定义和高级用法，请参考 internal 包和各 Loader 源码注释。
