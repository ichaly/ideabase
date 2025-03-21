# GraphQL 元数据加载器

## 概述

元数据加载器是 GraphQL 模块的核心组件，负责从多种来源加载数据库结构信息，合并后转换为内存中的数据结构，供 GraphQL 查询引擎使用。它提供了灵活的配置选项，支持多种数据源，以及丰富的名称转换和过滤功能。

## 主要功能

### 多源数据加载

元数据加载器采用了智能的多级加载机制：

1. **环境感知加载**：
   - 开发环境：直接从数据库加载最新的表结构
   - 生产环境：从预设文件加载固定的表结构（避免不必要的数据库连接）

2. **配置增强**：
   - 无论在哪种环境，都会从配置中加载可配置的元数据结构
   - 配置中的定义会与基础元数据合并，配置拥有最高优先级

3. **合并策略**：
   - 对于同名的类和字段，配置会覆盖基础元数据
   - 配置中定义的新类和字段会被添加到元数据中
   - 虚拟表和字段只能通过配置创建

### 数据源类型

元数据加载器使用以下数据源：

1. **数据库源（Database）**：
   - 直接从 PostgreSQL 数据库中读取表结构信息
   - 自动获取表、列、主键和外键关系
   - 支持从表和列的注释中读取描述信息

2. **文件源（File）**：
   - 从 JSON 文件读取预先生成的元数据缓存
   - 适用于生产环境，避免频繁连接数据库
   - 支持从开发环境导出元数据供生产使用

3. **配置源（Config）**：
   - 直接从应用配置中读取元数据定义
   - 完全自定义的表和字段结构
   - 适用于虚拟表或复杂的自定义数据结构
   - 支持配置关联表
   - 可用于覆盖数据库或文件中的定义

### 元数据缓存

- 支持将从数据库读取的元数据序列化到 JSON 文件
- 可配置是否启用缓存功能
- 适用于无法直接连接数据库的环境（如无权限读取数据库结构）
- 特别注意元数据的Nodes字段里包含了类名和表名的双重索引,Class的Fields也做了字段名和列名的双重索引

### 命名转换

- **下划线转驼峰**：自动将数据库中的下划线命名转换为 GraphQL 友好的驼峰命名
- **表名前缀去除**：自动去除表名中的特定前缀（例如 `tbl_`、`app_` 等）
- **自定义映射**：支持通过配置手动指定表名和字段名的映射关系

### 过滤功能

- **表级过滤**：
  - 包含列表：仅加载指定的表（白名单）
  - 排除列表：排除指定的表（黑名单）

- **字段级过滤**：
  - 排除指定的字段（如敏感信息 `password`、`secret` 等）

### 关系处理

- 自动识别和处理外键关系
- 支持多种关系类型：一对多、多对一、多对多、递归关系
- 构建关系索引，便于查询时快速定位关联数据

## 数据结构

元数据加载器使用多层索引结构组织内存中的元数据：

1. **主索引**：`Nodes` - 类名到类定义的映射
2. **表名索引**：`tableToClass` - 表名到类名的映射
3. **原始表名索引**：`rawTableToClass` - 原始（转换前）表名到类名的映射
4. **关系索引**：`relationships` - 表名和列名到外键关系的多级映射

## 配置选项

元数据加载器支持以下配置选项：

```yaml
schema:
  # 元数据加载源：database, file, config
  source: database
  
  # 数据库 schema 名称（用于 database 源）
  schema: public
  
  # 是否启用下划线转驼峰命名
  enable-camel-case: true
  
  # 是否启用缓存
  enable-cache: false
  
  # 缓存文件路径（用于 file 源或 enable-cache 为 true 时）
  cache-path: ./metadata_cache.json
  
  # 表名前缀（用于去除，支持多个前缀）
  table-prefix: 
    - tbl_
    - app_
  
  # 要包含的表（空表示包含所有）
  include-tables: []
  
  # 要排除的表
  exclude-tables: []
  
  # 要排除的字段
  exclude-fields: []
  
  # 字段名映射（用于自定义命名）
  field-mapping: {}
  
  # 表名映射（用于自定义命名）
  table-mapping: {}
  
  # 数据类型映射
  mapping: {}
  
  # 默认分页限制
  default-limit: 10
```

## 使用示例

### 1. 配置不同环境

```go
import (
    "github.com/spf13/viper"
    "gorm.io/gorm"
    "github.com/ichaly/ideabase/gql"
)

func main() {
    // 配置
    v := viper.New()
    
    // 开发环境配置
    v.Set("debug", true) // 设置为开发环境，将从数据库加载元数据
    v.Set("schema.schema", "public")
    v.Set("schema.enable-camel-case", true)
    v.Set("schema.table-prefix", []string{"tbl_", "app_"})
    
    // 数据库连接（开发环境需要）
    db, _ := gorm.Open(postgres.Open(dsn), &gorm.Config{})
    
    // 创建元数据加载器
    meta, err := gql.NewMetadata(v, db)
    if err != nil {
        panic(err)
    }
    
    // 使用元数据
    // ...
}
```

### 2. 配置元数据增强

```yaml
# 配置文件示例 (config.yaml)
debug: false  # 生产环境

schema:
  cache-path: "./metadata_cache.json"  # 预设元数据文件

# 元数据定义 - 将与基础元数据合并
metadata:
  tables:
    - name: users
      display_name: User
      description: "用户信息表"
      primary_keys: ["id"]
      columns:
        - name: id
          display_name: id
          type: integer
          is_primary: true
          description: "用户ID"
        
        - name: user_name
          display_name: username
          type: string
          description: "用户名"
          
        # 虚拟字段示例
        - name: full_name
          display_name: fullName
          type: string
          description: "用户全名(虚拟字段)"
          # 没有对应的数据库列，会被视为虚拟字段
        
        # 外键关系示例
        - name: department_id
          display_name: departmentId
          type: integer
          description: "部门ID"
          foreign_key:
            table: departments  # 关联表
            column: id         # 关联列
            kind: many_to_one  # 关系类型
    
    # 完全虚拟的表示例
    - name: statistics_view
      display_name: Statistics
      description: "统计数据视图"
      columns:
        - name: total_users
          display_name: totalUsers
          type: integer
          description: "用户总数"
```

### 3. 使用过滤功能

```go
// 只包含指定表
v.Set("schema.include-tables", []string{"users", "posts", "comments"})

// 排除敏感字段
v.Set("schema.exclude-fields", []string{"password", "secret_key"})
```

## 高级功能

### 1. 自定义表名和字段名映射

```go
// 表名映射
v.Set("schema.table-mapping", map[string]string{
    "tbl_user_account": "User",
    "tbl_blog_post": "Article",
})

// 字段名映射
v.Set("schema.field-mapping", map[string]string{
    "tbl_user_account.user_name": "username",
    "created_at": "creationTime",
})
```

### 2. 虚拟字段和类

可以通过配置源创建不存在于数据库中的虚拟字段和类，用于实现复杂的数据关系或计算字段。

## 最佳实践

1. **开发环境**：
   - 启用 `debug` 模式，直接从数据库加载最新结构
   - 启用缓存功能，以便导出元数据供生产环境使用

2. **测试环境**：
   - 可以使用与开发环境类似的配置
   - 在数据库结构变更后清除缓存

3. **生产环境**：
   - 禁用 `debug` 模式，从预设文件加载结构
   - 配置统一的元数据文件位置
   - 使用配置元数据覆盖必要的类和字段定义

4. **安全考虑**：
   - 始终使用 `exclude-fields` 排除敏感字段
   - 使用 `include-tables` 严格控制可访问的表

## 扩展计划

1. **支持更多数据库**：
   - 除 PostgreSQL 外，添加对 MySQL、SQL Server 等数据库的支持
   
2. **增强的关系检测**：
   - 基于命名约定的隐式关系识别
   - 复杂关系类型支持（多对多中间表）
   
3. **元数据缓存优化**：
   - 增量更新机制
   - 基于 etag 的缓存验证

4. **模式版本控制**：
   - 跟踪和管理元数据变更
   - 提供向后兼容性支持