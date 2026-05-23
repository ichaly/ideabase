package internal

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/compress"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompressLevel_KoanfIntegration 测试在Koanf中使用CompressLevel
func TestCompressLevel_KoanfIntegration(t *testing.T) {
	type Config struct {
		Level CompressLevel `koanf:"level"`
	}

	testCases := []struct {
		name     string
		yamlData string
		expected compress.Level
	}{
		{
			name:     "字符串值LevelDisabled",
			yamlData: "level: LevelDisabled",
			expected: compress.LevelDisabled,
		},
		{
			name:     "字符串值LevelBestSpeed",
			yamlData: "level: LevelBestSpeed",
			expected: compress.LevelBestSpeed,
		},
		{
			name:     "字符串值LevelBestCompression",
			yamlData: "level: LevelBestCompression",
			expected: compress.LevelBestCompression,
		},
		{
			name:     "数字值-1",
			yamlData: "level: -1",
			expected: compress.LevelDisabled,
		},
		{
			name:     "数字值0",
			yamlData: "level: 0",
			expected: compress.LevelDefault,
		},
		{
			name:     "数字值1",
			yamlData: "level: 1",
			expected: compress.LevelBestSpeed,
		},
		{
			name:     "数字值2",
			yamlData: "level: 2",
			expected: compress.LevelBestCompression,
		},
		{
			name:     "无效值",
			yamlData: "level: invalid",
			expected: compress.LevelDefault,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k := koanf.New(".")
			err := k.Load(rawbytes.Provider([]byte(tc.yamlData)), yaml.Parser())
			assert.NoError(t, err)

			var cfg Config
			err = k.Unmarshal("", &cfg)
			assert.NoError(t, err)
			assert.Equal(t, compress.Level(cfg.Level), tc.expected)
		})
	}
}

func TestFiberConfigProxy_KoanfIntegration(t *testing.T) {
	k := koanf.New(".")
	err := k.Load(rawbytes.Provider([]byte(`
proxy_header: X-Forwarded-For
trust_proxy: true
enable_ip_validation: true
trust_proxy_config:
  proxies:
    - 172.18.0.0/16
  private: true
  loopback: true
`)), yaml.Parser())
	require.NoError(t, err)

	var cfg FiberConfig
	err = k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "mapstructure",
	})
	require.NoError(t, err)

	assert.Equal(t, fiber.HeaderXForwardedFor, cfg.ProxyHeader)
	assert.True(t, cfg.TrustProxy)
	assert.True(t, cfg.EnableIPValidation)
	assert.Equal(t, []string{"172.18.0.0/16"}, cfg.TrustProxyConfig.Proxies)
	assert.True(t, cfg.TrustProxyConfig.Private)
	assert.True(t, cfg.TrustProxyConfig.Loopback)
}
