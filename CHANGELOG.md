
## app v0.0.1 (2025-07-30)
- refactor(std): 重构 cache.go 替换 util包为 utl 包
- refactor(project): 重构项目模块和依赖
- refactor: 将 app 目录重命名为 cli
- refactor: 修改配置文件路径获取方式
- build: 更新模块版本并优化安装脚本
- chore: 更新Go版本至1.23，并调整相关模块的go.mod文件以保持一致性
- refactor: 重构IOC容器，添加Add和Get方法以增强依赖管理的灵活性
- feat: 修改命令行工具名称为ibc，并更新启动命令的配置文件标志以增强可读性
- feat: 添加应用的基本结构，包括主入口、命令行工具和IOC容器，以支持后续功能开发

## cmd v0.0.1 (2025-07-30)
- refactor(std): 重构 cache.go 替换 util包为 utl 包
- refactor(project): 重构项目模块和依赖

## ioc v0.0.1 (2025-07-30)
- refactor(project): 重构项目模块和依赖
- refactor: 重命名为
- build: 添加UPX压缩选项并重构构建脚本以支持多平台编译
- refactor: 移除Compiler类并将方言选择逻辑移至Executor
- feat: 增强GraphQL编译器，支持方言注册和上下文对象池以优化性能
- feat: 添加GraphQL执行器及相关配置，优化请求处理逻辑并整合数据库与缓存配置
- refactor: 优化配置模块，使用fx.Annotate替代闭包传递Option参数，并在容器初始化中添加NewFiber提供者
- feat: 添加Bootstrap函数和测试用例以支持插件和中间件的初始化
- chore: 更新Go版本至1.23，并调整相关模块的go.mod文件以保持一致性
- refactor: 重构IOC容器，添加Add和Get方法以增强依赖管理的灵活性
- feat: 添加应用的基本结构，包括主入口、命令行工具和IOC容器，以支持后续功能开发

## log v0.0.1 (2025-07-30)
- chore: 更新多个模块的依赖版本，提升项目的稳定性和安全性
- refactor: 将Default函数重命名为GetDefault并调整返回类型
- chore: 更新Go版本至1.23，并调整相关模块的go.mod文件以保持一致性
- feat: 支持自关联关系类型识别和处理
- chore: 统一项目Go版本至1.22并更新依赖
- chore: restructure project
