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
- ✅ PostgreSQL (已实现)
- 🔜 MySQL (计划中)

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