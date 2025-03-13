# 元数据配置系统

## 概述

元数据配置系统是IdeaBase的核心组件，负责从多种来源加载数据库结构信息，合并后转换为内存中的数据结构，供GraphQL查询引擎使用。它提供了灵活的配置选项，支持多种数据源，以及丰富的名称转换和过滤功能。

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

### 表和字段别名机制

1. **表别名**：
   - 支持为一个数据库表配置多个GraphQL类型（别名）
   - 不同的别名可以拥有不同的字段集合和关系配置
   - 可以通过字段过滤机制为不同别名类提供不同的字段视图

2. **字段别名**：
   - 可以为数据库列自定义不同的GraphQL字段名
   - 配置不同的字段描述和属性

### 自定义Resolver机制

1. **类级别Resolver**：
   - 可以为整个类指定自定义的Resolver
   - 适用于虚拟表或需要特殊逻辑处理的类型

2. **字段级别Resolver**：
   - 可以为单个字段指定自定义的Resolver
   - 适用于需要特殊计算或外部数据获取的字段

### 多对多关系增强

1. **完整的中间表支持**：
   - 可以将中间表配置为独立的实体
   - 支持为中间表定义额外的字段和属性
   - 使用`through`配置中的`class_name`和`fields`属性定义

2. **灵活的关系映射**：
   - 支持在中间表上定义额外的关系
   - 可以配置中间表字段的自定义Resolver

### 字段过滤和视图

1. **字段排除机制**：
   - 通过`exclude_fields`配置排除特定字段
   - 适用于创建无敏感信息的公开视图

2. **字段包含机制**：
   - 通过`include_fields`配置仅包含特定字段
   - 适用于创建精简视图，简化配置

3. **同表不同视图**：
   - 基于同一数据库表创建多个不同的GraphQL类型
   - 每个视图可以有不同的字段集合和处理逻辑

## 配置结构

元数据使用以下配置结构：

```yaml
metadata:
  classes:
    # 类定义
    ClassName:
      table: "table_name"  # 数据库表名
      description: "类描述"
      primary_keys: ["id"]
      resolver: "ClassResolver"  # 类级别Resolver
      
      # 字段过滤
      exclude_fields: ["password", "secret"]  # 排除敏感字段
      # 或者使用包含字段（二选一，include_fields优先）
      include_fields: ["id", "name", "email"]  # 仅包含这些字段
      
      # 字段定义
      fields:
        id:
          column: "id"  # 数据库列名
          type: "integer"
          is_primary: true
          description: "主键ID"
        
        name:
          column: "user_name"  # 字段名与列名不同
          type: "string"
          description: "用户名"
          resolver: "NameResolver"  # 字段级别Resolver
        
        # 虚拟字段
        fullName:
          virtual: true
          type: "string"
          description: "用户全名"
          resolver: "FullNameResolver"
        
        # 关系字段
        department:
          column: "department_id"
          type: "integer"
          description: "部门ID"
          relation:
            target_class: "Department"
            target_field: "id"
            type: "many_to_one"
            reverse_name: "employees"  # 反向关系名称
```

## 使用示例

### 1. 基本类定义

```yaml
metadata:
  classes:
    User:
      table: users
      description: "用户信息"
      primary_keys: ["id"]
      fields:
        id:
          column: id
          type: integer
          is_primary: true
        
        username:
          column: user_name
          type: string
          description: "用户名"
        
        email:
          column: email
          type: string
          description: "邮箱"
```

### 2. 同表不同视图（屏蔽敏感字段）

```yaml
metadata:
  classes:
    # 完整用户视图（管理员使用）
    User:
      table: users
      description: "用户完整信息"
      fields:
        id:
          type: integer
          is_primary: true
        
        username:
          type: string
        
        password:
          type: string
        
        email:
          type: string
        
        phone:
          type: string
    
    # 公开用户视图（去除敏感信息）
    PublicUser:
      table: users
      description: "用户公开信息"
      exclude_fields: ["password", "phone"]
      fields:
        email:
          description: "电子邮箱(已脱敏)"
          resolver: "MaskedEmailResolver"
```

### 3. 使用包含字段简化配置

```yaml
metadata:
  classes:
    # 使用include_fields简化配置
    MiniUser:
      table: users
      description: "用户简要信息"
      include_fields: ["id", "username"]
      # 添加自定义字段
      fields:
        displayName:
          virtual: true
          type: string
          resolver: "DisplayNameResolver"
```

### 4. 虚拟表配置

```yaml
metadata:
  classes:
    Statistics:
      virtual: true
      description: "统计数据"
      resolver: "StatisticsResolver"
      fields:
        totalUsers:
          type: integer
          description: "用户总数"
          resolver: "CountUsersResolver"
        
        activeUsers:
          type: integer
          description: "活跃用户数"
          resolver: "CountActiveUsersResolver"
```

### 5. 多对多关系配置

```yaml
metadata:
  classes:
    Post:
      table: posts
      description: "文章"
      fields:
        id:
          type: integer
          is_primary: true
        
        title:
          type: string
        
        tags:
          virtual: true
          description: "文章标签"
          relation:
            target_class: "Tag"
            target_field: "id"
            type: "many_to_many"
            through:
              table: "post_tags"  # 中间表
              source_key: "post_id"  # 中间表中指向源表的外键
              target_key: "tag_id"  # 中间表中指向目标表的外键
              class_name: "PostTag"  # 将中间表作为独立实体
              fields:  # 中间表的额外字段
                created_at:
                  type: timestamp
                  description: "标签添加时间"
```

## 元数据结构

元数据加载器使用多层索引结构组织内存中的元数据：

1. **主索引**：`Nodes` - 类名到类定义的映射
2. **表名索引**：同时支持通过表名查找对应的类定义
3. **自定义Resolver**：类和字段的`Resolver`属性 - 自定义处理逻辑

## 最佳实践

1. **类名与表名映射**：
   - 使用类名作为配置键，而不是表名
   - 表名通过`table`属性指定
   - 多个别名可通过配置多个指向同一表的类定义

2. **别名管理**：
   - 避免过多的别名导致混淆
   - 给每个别名提供清晰的描述
   - 使用适当的字段过滤保持视图的清晰

3. **Resolver设计**：
   - 优先使用字段级别的Resolver而不是类级别
   - 对于复杂逻辑，使用专门的Resolver模块

4. **字段过滤使用**：
   - 当需要排除少量字段时，使用`exclude_fields`
   - 当需要包含少量字段时，使用`include_fields`更简洁
   - `include_fields`优先级高于`exclude_fields`

5. **多对多关系**：
   - 为中间表提供明确的类名
   - 明确定义正向和反向关系
   
6. **表名前缀处理**：
   - 配置全局的表名前缀去除规则
   - 保持GraphQL类型命名简洁

## 注意事项

1. 同表的不同视图类在配置时，系统会自动基于表名查找基类，复制其字段定义

2. 排除或包含字段的过滤会在字段复制之后、字段覆盖之前应用

3. 字段定义中的显式配置总是会覆盖从基类复制的定义

4. 虚拟字段只能通过配置创建，无法从数据库自动加载

5. 为确保性能，推荐在非开发环境使用文件源加载元数据