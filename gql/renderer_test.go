package gql

import (
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/utl"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// 尝试从测试数据库获取元数据
func getTestMetadata(t *testing.T) (*Metadata, error) {
	// 初始化测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 数据库已连接，创建并加载元数据
	t.Log("成功连接到数据库，准备加载元数据")

	// 设置Viper配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.schema", "public")
	v.Set("schema.enable-camel-case", true)

	// 创建元数据并从数据库加载
	meta, err := NewMetadata(v, db)
	if err != nil {
		return nil, fmt.Errorf("从数据库加载元数据失败: %w", err)
	}

	t.Logf("成功加载元数据，包含 %d 个类定义", len(meta.Nodes))
	return meta, nil
}

func TestRenderer_Generate(t *testing.T) {
	// 从数据库或模拟数据获取元数据
	meta, err := getTestMetadata(t)
	if err != nil {
		t.Skipf("跳过测试: %v", err)
		return
	}

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 生成schema
	schema, err := renderer.Generate()

	// 验证生成成功
	assert.NoError(t, err)
	assert.NotEmpty(t, schema)

	// 验证schema包含预期部分
	assert.Contains(t, schema, "type Query {")
	assert.Contains(t, schema, "type Mutation {")
	assert.Contains(t, schema, "scalar DateTime")

	// 验证基本类型是否存在
	if len(meta.Nodes) > 0 {
		// 获取第一个类名进行验证
		var className string
		for name := range meta.Nodes {
			className = name
			break
		}
		t.Logf("验证生成的schema中包含%s类型", className)
		assert.Contains(t, schema, "type "+className+" {")
	}
}
