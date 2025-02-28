package gtw

// Gateway 网关接口
type Gateway interface {
	// Start 启动网关
	Start() error
	// Stop 停止网关
	Stop() error
}
