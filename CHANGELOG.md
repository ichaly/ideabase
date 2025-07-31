
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

## std v0.0.1 (2025-07-30)
- build(std): 升级 fx 和 dig 依赖版本&优化 release.sh脚本中的错误提示和信息输出
- refactor(std): 重构 cache.go 替换 util包为 utl 包
- refactor: 重命名为
- chore: 更新多个模块的依赖版本，提升项目的稳定性和安全性
- test: 添加健康检查端点并更新插件方法命名
- refactor: 重命名插件接口方法以提升语义清晰度
- refactor: 优化CSRF中间件配置逻辑，仅在非调试模式下启用以增强安全性
- refactor: 更新配置文件，调整根路径注释及字段名称以提升可读性和一致性
- feat: 添加GraphQL执行器及相关配置，优化请求处理逻辑并整合数据库与缓存配置
- refactor: 替换为三方fiberzerolog日志中间件
- refactor: 移除自定义的幂等性检查逻辑
- feat: 更新Fiber应用配置，优化中间件使用，添加压缩级别解析功能及相关测试用例
- feat: 增强Fiber应用配置，添加超时、压缩和中间件设置，优化测试用例以验证请求超时处理
- feat: 添加配置项的持续时间解析测试用例，验证不同格式的持续时间值的正确性
- refactor: 移除全局超时处理逻辑，简化Fiber应用配置以提升可维护性
- feat: 添加安全填充功能以支持Cookie加密中间件，优化密钥处理逻辑并增加相关测试用例
- feat: 添加多个中间件测试用例，包括异常恢复、CORS、请求ID、Cookie加密、压缩、ETag和日志中间件，增强代码的可测试性
- feat: 添加Cookie加密中间件支持，更新应用配置以包含加密密钥
- chore: 更新go.mod文件，移除直接依赖的github.com/google/uuid，添加为间接依赖以优化依赖管理
- refactor: 重构Fiber应用配置，移除冗余的幂等性中间件实现，添加测试用例以增强代码可维护性
- feat: 添加幂等性中间件及其配置，支持请求的唯一性处理和结果缓存
- chore: 更新gql和std模块中的依赖项版本以保持一致性和最新状态
- feat: 添加Bootstrap函数和测试用例以支持插件和中间件的初始化
- chore: 更新Go版本至1.23，并调整相关模块的go.mod文件以保持一致性
- chore: 更新go.mod文件，添加新依赖项以支持项目功能扩展
- refactor: 移除示例配置文件，优化测试用例，增强Konfig配置管理器的功能和可维护性
- test: 增加环境变量处理相关的单元测试，验证不同类型和优先级的环境变量加载逻辑
- feat: 添加多种加载策略到Konfig配置管理器，支持从文件、环境变量和默认值加载配置
- refactor: 重构Konfig配置管理器，整合配置文件监听功能，优化配置变更处理逻辑
- refactor: 将项目中的viper配置替换为Konfig，提升配置管理一致性
- feat: 在Konfig中添加SetDefault和SetDefaults方法，支持默认值设置与加载
- refactor: 统一Konfig方法接收者名称为my，提升代码一致性和可读性
- refactor: 更新Konfig构造函数，支持通过WithFilePath选项传递配置文件路径，优化配置加载逻辑
- refactor: 移除测试中对koanf依赖的跳过逻辑，优化UnmarshalKey方法以支持配置解析
- feat: 添加Konfig配置管理器封装koanf并兼容viper
- feat: 添加koanf配置工具及其监视器，支持动态配置加载与环境变量管理
- feat: 支持自关联关系类型识别和处理
- feat: 更新配置结构并设置默认应用根目录
- chore: 更新 gql 和 std 模块依赖
- chore: 移除 std 模块中的本地依赖
- chore: 更新 Go 模块依赖和版本
- chore: 统一项目Go版本至1.22并更新依赖
- feat: 为配置结构添加调试模式支持
- refactor: 优化Viper配置合并逻辑，简化错误处理
- refactor: Enhance Viper configuration management with flexible options and robust error handling
- chore: restructure project

## utl v0.0.1 (2025-07-30)
- feat: 新增Must函数处理错误
- chore: 更新多个模块的依赖版本，提升项目的稳定性和安全性
- feat: 新增JSON序列化工具函数，提供标准化的JSON解析和序列化功能
- feat: 添加安全填充功能以支持Cookie加密中间件，优化密钥处理逻辑并增加相关测试用例
- build: 更新模块版本并优化安装脚本
- chore: 更新Go版本至1.23，并调整相关模块的go.mod文件以保持一致性
- chore: 更新go.mod文件，添加新依赖项以支持项目功能扩展
- refactor: 添加泛型函数以获取和排序map的键，提升代码复用性
- chore: 统一项目Go版本至1.22并更新依赖
- refactor: 用统一的键检索方法增强TeeMap
- docs: 为工具类函数添加详细的注释文档
- refactor: Improve file utility functions with predefined error constants and deferred error handling
- refactor: Improve utility functions with enhanced error handling and performance
- chore: restructure project

## app v0.0.2 (2025-07-31)
- chore(app): release 0.0.1
- refactor(std): 重构 cache.go 替换 util包为 utl 包
- refactor(project): 重构项目模块和依赖
- refactor: 将 app 目录重命名为 cli
- refactor: 修改配置文件路径获取方式
- build: 更新模块版本并优化安装脚本
- chore: 更新Go版本至1.23，并调整相关模块的go.mod文件以保持一致性
- refactor: 重构IOC容器，添加Add和Get方法以增强依赖管理的灵活性
- feat: 修改命令行工具名称为ibc，并更新启动命令的配置文件标志以增强可读性
- feat: 添加应用的基本结构，包括主入口、命令行工具和IOC容器，以支持后续功能开发
