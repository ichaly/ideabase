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
   - 通过`aliases`数组属性定义多个别名

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

### 虚拟表和字段

1. **虚拟表配置**：
   - 完全不依赖数据库的虚拟表定义
   - 可以配置虚拟表的字段、关系和Resolver

2. **虚拟字段配置**：
   - 在真实表上添加虚拟字段
   - 支持计算字段和关系字段

## 配置示例

以下是元数据配置的示例，展示了各种功能的使用方法：

```yaml
metadata:
  classes:
    # 基础类配置
    User:
      table: "users"  # 原始表名
      description: "用户信息"
      primary_keys: ["id"]
      resolver: "userResolver"  # 类级别自定义Resolver
      aliases: ["Customer", "Member"]  # 表别名列表
      fields:
        id:
          column: "id"  # 原始列名
          type: "integer"
          is_primary: true
          description: "用户ID"
        
        username:
          column: "user_name"
          type: "string"
          description: "用户名"
        
        # 虚拟字段示例
        fullName:
          virtual: true
          type: "string"
          description: "用户全名(计算字段)"
          resolver: "fullNameResolver"  # 字段级别自定义Resolver
        
        # 关系字段示例
        department:
          column: "department_id"
          type: "integer"
          description: "部门ID"
          relation:
            target_class: "Department"
            target_field: "id"
            type: "many_to_one"
    
    # 多对多关系示例
    Post:
      table: "posts"
      description: "文章"
      fields:
        id:
          type: "integer"
          is_primary: true
        
        title:
          type: "string"
        
        # 多对多关系
        tags:
          virtual: true
          relation:
            target_class: "Tag"
            target_field: "id"
            type: "many_to_many"
            through:
              table: "post_tags"  # 中间表名
              source_key: "post_id"  # 中间表中指向源表的外键
              target_key: "tag_id"  # 中间表中指向目标表的外键
              class_name: "PostTag"  # 将中间表作为独立实体
              fields:  # 中间表的额外字段
                id:
                  type: "integer"
                  is_primary: true
                
                created_at:
                  type: "timestamp"
    
    # 虚拟表示例
    Statistics:
      virtual: true
      description: "统计数据(虚拟表)"
      resolver: "statisticsResolver"
      fields:
        totalUsers:
          type: "integer"
          description: "用户总数"
          resolver: "countUsersResolver"
        
        activeUsers:
          type: "integer"
          description: "活跃用户数"
          resolver: "countActiveUsersResolver"
```

## 元数据结构

元数据加载器使用多层索引结构组织内存中的元数据：

1. **主索引**：`Nodes` - 类名到类定义的映射
2. **类别名**：类的`Aliases`属性 - 类的额外名称列表
3. **自定义Resolver**：类和字段的`Resolver`属性 - 自定义处理逻辑

## 最佳实践

1. **类名与表名映射**：
   - 使用类名作为配置键，而不是表名
   - 表名通过`table`属性指定
   - 使用`aliases`定义多个别名而不是重复定义类

2. **别名管理**：
   - 避免过多的别名导致混淆
   - 给每个别名提供清晰的描述

3. **Resolver设计**：
   - 优先使用字段级别的Resolver而不是类级别
   - 对于复杂逻辑，使用专门的Resolver模块

4. **性能优化**：
   - 尽量减少虚拟字段，特别是需要复杂计算的字段
   - 合理使用中间表自定义字段