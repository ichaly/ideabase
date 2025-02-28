# IdeaBase

## 项目简介
IdeaBase是一个基于Golang开发的GraphQL到SQL编译中间件，能够自动将GraphQL查询转换为高效的SQL语句，简化数据库访问层开发。

## 核心特性
- 🚀 高性能GraphQL到SQL的编译转换
- 🔄 支持复杂的CRUD操作和表关系
- 🛠️ 配置化虚拟表和自定义resolver
- 💾 内置缓存机制和连接池管理
- 🔌 模块化设计，支持独立使用或服务化部署

## 数据库支持
- ✅ PostgreSQL 9.6+ (已实现)
  - 利用JSON聚合功能实现高效单查询
  - 需要PostgreSQL 9.6或更高版本
- ✅ MySQL 8.0+ (已实现)
  - 利用CTE和JSON函数实现高效单查询
  - 需要MySQL 8.0或更高版本

## 版本兼容性说明
本项目对数据库版本有严格要求，以充分利用现代数据库特性：
- **PostgreSQL**: 要求9.6+版本，利用`json_agg`和`json_build_object`等函数
- **MySQL**: 要求8.0+版本，利用CTE(WITH语句)和JSON_OBJECT等函数

使用低于要求版本的数据库将导致初始化失败，并返回明确的错误信息。

详细的兼容性说明请参考[数据库兼容性文档](doc/database-compatibility.md)。

## 项目结构
```
IdeaBase/
├── gql/    # GraphQL解析与SQL转换核心
├── svc/    # 服务发布与API暴露
├── gtw/    # 多服务整合与网关
├── std/    # 基础设施与共享组件
└── utl/    # 通用工具集合
```

## 快速开始
请参考`project-description-chinese.mdc`文件获取详细的项目开发指南。

## 参考项目
本项目参考了[GraphJin](https://github.com/dosco/graphjin)的设计理念，并进行了重新实现和扩展。

## 开发理念
- 最小增量开发
- 完善的单元测试
- 清晰的模块边界
- 高性能设计