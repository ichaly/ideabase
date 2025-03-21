# IdeaBase

IdeaBase 是一个高性能的 GraphQL 到 SQL 编译中间件，专注于将 GraphQL 查询高效地转换为优化的 SQL 语句。

## 项目特点

- 高效的 GraphQL 到 SQL 的转换引擎
- 支持复杂的表关系处理（一对一、一对多、多对多）
- 可配置的元数据管理
- 完善的单元测试覆盖
- 模块化设计，易于扩展

## 项目结构

```
IdeaBase/
├── gql/                    # GraphQL核心处理模块
│   ├── compiler/          # GraphQL编译器
│   ├── executor/          # SQL执行器
│   ├── metadata/          # 元数据管理
│   ├── renderer/          # SQL渲染器
│   └── resolver/          # 自定义解析器
├── svc/                    # 服务层实现
├── gtw/                    # API网关层
├── std/                    # 标准库和基础设施
│   ├── konfig.go          # 配置管理
│   ├── connect.go         # 数据库连接
│   └── entity.go          # 基础实体定义
├── utl/                    # 通用工具集
│   ├── maps.go            # Map相关工具
│   ├── strings.go         # 字符串处理
│   └── files.go           # 文件操作
└── cfg/                    # 配置文件目录
```

## 核心功能

### GraphQL 编译器 (gql/compiler)
- GraphQL 查询解析和验证
- AST 转换
- 查询优化

### SQL 渲染器 (gql/renderer)
- 支持复杂的表关系处理
- SQL 语句优化
- 多数据库方言支持

### 元数据管理 (gql/metadata)
- 表结构和关系配置
- 字段映射
- 权限控制

## 技术栈

- Go 1.21+
- GraphQL Parser: github.com/vektah/gqlparser/v2
- 配置管理: github.com/knadh/koanf
- 工具库: github.com/duke-git/lancet/v2
- JSON处理: github.com/json-iterator/go

## 开发指南

### 环境要求

- Go 1.21 或更高版本
- PostgreSQL 14+ 或 MySQL 8+
- Docker (可选，用于本地开发)

### 本地开发

1. 克隆仓库
```bash
git clone https://github.com/your-org/ideabase.git
cd ideabase
```

2. 安装依赖
```bash
go work init
go work use ./gql ./svc ./std ./utl
go mod download
```

3. 运行测试
```bash
go test ./...
```

## 贡献指南

1. Fork 项目
2. 创建特性分支
3. 提交变更
4. 推送到分支
5. 创建 Pull Request

## 许可证

[License Name] - 详见 LICENSE 文件