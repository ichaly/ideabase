package svc

// Service 服务接口
type Service interface {
	// Start 启动服务
	Start() error
	// Stop 停止服务
	Stop() error
}
