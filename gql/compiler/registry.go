package compiler

// 方言自注册表（database/sql驱动同款模式）：
// 方言包在init中调用Register，应用空白导入方言包即可启用，
// 新增数据库支持只需新增方言包，不修改编译器与现有方言的任何代码
var registry []Dialect

// Register 注册方言实现（在方言包init中调用）
func Register(dialect Dialect) {
	registry = append(registry, dialect)
}

// Dialects 返回所有已注册的方言
func Dialects() []Dialect {
	return registry
}
