package std

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/ichaly/ideabase/std/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockPlugin 模拟插件
type MockPlugin struct {
	mock.Mock
}

func (m *MockPlugin) Path() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockPlugin) Bind(router fiber.Router) {
	m.Called(router)
}

// MockLifecycle 模拟生命周期
type MockLifecycle struct {
	mock.Mock
	startFuncs []func(context.Context) error
	stopFuncs  []func(context.Context) error
}

func (m *MockLifecycle) Append(start, stop func(context.Context) error) {
	m.Called(start, stop)
	m.startFuncs = append(m.startFuncs, start)
	m.stopFuncs = append(m.stopFuncs, stop)
}

// 执行生命周期钩子
func (m *MockLifecycle) executeHooks(t *testing.T) {
	for i := range m.startFuncs {
		err := m.startFuncs[i](context.Background())
		assert.NoError(t, err)

		// 给异步操作一点时间
		time.Sleep(10 * time.Millisecond)

		err = m.stopFuncs[i](context.Background())
		assert.NoError(t, err)
	}
}

// 测试用配置
func createTestConfig() *Config {
	return &Config{
		AppConfig: internal.AppConfig{
			Name: "测试应用",
			Port: "8080",
		},
		Mode: "test",
	}
}

func TestBootstrap(t *testing.T) {
	// 初始化测试数据
	app := fiber.New()
	lifecycle := &MockLifecycle{}
	config := createTestConfig()

	// 创建mock插件
	plugin := &MockPlugin{}
	plugin.On("Path").Return("/api")
	plugin.On("Bind", mock.Anything).Return()

	// 创建mock中间件
	filter := &MockPlugin{}
	filter.On("Path").Return("/")
	filter.On("Bind", mock.Anything).Return()

	// 设置生命周期期望
	lifecycle.On("Append", mock.AnythingOfType("func(context.Context) error"), mock.AnythingOfType("func(context.Context) error")).Return()

	// 调用被测函数
	Bootstrap([]Plugin{plugin}, []Plugin{filter}, lifecycle, config, app)

	// 验证路由是否正确设置
	// 添加测试路由
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("测试成功")
	})

	// 发送测试请求
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 验证mock对象
	plugin.AssertExpectations(t)
	filter.AssertExpectations(t)
	lifecycle.AssertExpectations(t)

	// 测试生命周期钩子
	lifecycle.executeHooks(t)
}

// 测试复杂路由场景
func TestBootstrap_ComplexRoutes(t *testing.T) {
	// 初始化测试数据
	app := fiber.New()
	lifecycle := &MockLifecycle{}
	config := createTestConfig()

	// 创建多个具有不同基础路径的插件
	apiPlugin := &MockPlugin{}
	apiPlugin.On("Path").Return("/api")
	apiPlugin.On("Bind", mock.Anything).Return()

	userPlugin := &MockPlugin{}
	userPlugin.On("Path").Return("/api/users")
	userPlugin.On("Bind", mock.Anything).Return()

	// 具有重复基础路径的插件（测试缓存）
	authPlugin := &MockPlugin{}
	authPlugin.On("Path").Return("/api/auth")
	authPlugin.On("Bind", mock.Anything).Return()

	authPlugin2 := &MockPlugin{}
	authPlugin2.On("Path").Return("/api/auth")
	authPlugin2.On("Bind", mock.Anything).Return()

	// 创建中间件
	logFilter := &MockPlugin{}
	logFilter.On("Path").Return("/")
	logFilter.On("Bind", mock.Anything).Return()

	// 设置生命周期期望
	lifecycle.On("Append", mock.AnythingOfType("func(context.Context) error"), mock.AnythingOfType("func(context.Context) error")).Return()

	// 调用被测函数
	Bootstrap([]Plugin{apiPlugin, userPlugin, authPlugin, authPlugin2}, []Plugin{logFilter}, lifecycle, config, app)

	// 验证所有mock对象的调用
	apiPlugin.AssertExpectations(t)
	userPlugin.AssertExpectations(t)
	authPlugin.AssertExpectations(t)
	authPlugin2.AssertExpectations(t)
	logFilter.AssertExpectations(t)
	lifecycle.AssertExpectations(t)
}

// 测试空路径情况
func TestBootstrap_EmptyBasePath(t *testing.T) {
	app := fiber.New()
	lifecycle := &MockLifecycle{}
	config := createTestConfig()

	// 创建基础路径为空的插件
	emptyPlugin := &MockPlugin{}
	emptyPlugin.On("Path").Return("")
	emptyPlugin.On("Bind", mock.Anything).Return()

	// 设置生命周期期望
	lifecycle.On("Append", mock.AnythingOfType("func(context.Context) error"), mock.AnythingOfType("func(context.Context) error")).Return()

	// 调用被测函数
	Bootstrap([]Plugin{emptyPlugin}, []Plugin{}, lifecycle, config, app)

	// 验证mock对象
	emptyPlugin.AssertExpectations(t)
	lifecycle.AssertExpectations(t)

	// 验证路由是否正确设置
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("根路径")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
