package run

// Runner 运行器接口
type Runner interface {
	// Run 运行任务
	Run() error
	// Stop 停止任务
	Stop() error
}
