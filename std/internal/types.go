package internal

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2/middleware/compress"
)

// AppConfig 应用程序配置信息
type AppConfig struct {
	Name       string       `mapstructure:"name"`        // 应用名称
	Port       string       `mapstructure:"port"`        // 服务端口
	Root       string       `mapstructure:"root"`        // 应用根目录路径
	Cache      *DataSource  `mapstructure:"cache"`       // 缓存数据源配置
	Database   *DataSource  `mapstructure:"database"`    // 数据库配置信息
	EncryptKey string       `mapstructure:"encrypt-key"` // Cookie加密密钥
	Fiber      *FiberConfig `mapstructure:"fiber"`       // Fiber框架配置
}

// DataSource 数据源配置信息
type DataSource struct {
	Uri      string       `mapstructure:"uri"`      // 完整连接URI
	Host     string       `mapstructure:"host"`     // 数据库主机地址
	Port     int          `mapstructure:"port"`     // 数据库端口
	Name     string       `mapstructure:"name"`     // 数据库名称（如数据库名）
	Dialect  string       `mapstructure:"dialect"`  // 数据库方言（如mysql、postgres）
	Username string       `mapstructure:"username"` // 访问账号
	Password string       `mapstructure:"password"` // 访问密码
	Sources  []DataSource `mapstructure:"sources"`  // 主数据源列表（用于分片/集群）
	Replicas []DataSource `mapstructure:"replicas"` // 副本数据源列表（用于读写分离）
}

// FiberConfig 包含Fiber应用的可配置参数
type FiberConfig struct {
	// 基础设置
	IdleTimeout  time.Duration `mapstructure:"idle_timeout"`  // 空闲超时时间
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`  // 读取超时时间
	WriteTimeout time.Duration `mapstructure:"write_timeout"` // 写入超时时间
	ServerHeader string        `mapstructure:"server_header"` // 服务器响应头

	// 压缩中间件设置
	CompressLevel CompressLevel `mapstructure:"compress_level"` // 压缩级别

	// 幂等性中间件设置
	IdempotencyLifetime  time.Duration `mapstructure:"idempotency_lifetime"`   // 幂等性键生存时间
	IdempotencyKeyHeader string        `mapstructure:"idempotency_key_header"` // 幂等性键头部

	// CSRF中间件设置
	CSRFKeyLookup      string        `mapstructure:"csrf_key_lookup"`       // CSRF令牌查找位置
	CSRFCookieName     string        `mapstructure:"csrf_cookie_name"`      // CSRF Cookie名称
	CSRFCookieSameSite string        `mapstructure:"csrf_cookie_same_site"` // CSRF Cookie同源策略
	CSRFExpiration     time.Duration `mapstructure:"csrf_expiration"`       // CSRF令牌过期时间

	// 限流中间件设置
	LimiterMax        int           `mapstructure:"limiter_max"`        // 请求限制数量
	LimiterExpiration time.Duration `mapstructure:"limiter_expiration"` // 限制窗口时间

	// 健康检查中间件设置
	LivenessEndpoint  string `mapstructure:"liveness_endpoint"`  // 存活检查端点
	ReadinessEndpoint string `mapstructure:"readiness_endpoint"` // 就绪检查端点
}

// CompressLevel 自定义的压缩级别类型
type CompressLevel compress.Level

// UnmarshalText 实现TextUnmarshaler接口
func (my *CompressLevel) UnmarshalText(text []byte) error {
	str := string(text)
	// 字符串处理
	switch str {
	case "levelDisabled", "LevelDisabled":
		*my = CompressLevel(compress.LevelDisabled)
	case "levelBestSpeed", "LevelBestSpeed":
		*my = CompressLevel(compress.LevelBestSpeed)
	case "levelBestCompression", "LevelBestCompression":
		*my = CompressLevel(compress.LevelBestCompression)
	default:
		// 尝试解析字符串数字
		if num, err := strconv.Atoi(str); err == nil {
			level := CompressLevel(num)
			// 检查值是否在有效范围内
			if level >= CompressLevel(compress.LevelDisabled) && level <= CompressLevel(compress.LevelBestCompression) {
				*my = level
				return nil
			}
		}
		*my = CompressLevel(compress.LevelDefault)
	}
	return nil
}
