package std

import (
	"encoding/json"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestValidatorErrorSerializedResult(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	type payload struct {
		Username string `validate:"required" label:"用户名"`
		Age      int    `validate:"gte=18" label:"年龄"`
	}

	gotErr := v.Struct(payload{Age: 16})
	require.Error(t, gotErr)

	ex := NewException(fiber.StatusBadRequest).
		WithMessage("请求参数错误").
		WithError(gotErr)

	res := Result{Errors: []*Exception{ex}}

	raw, err := json.Marshal(res)
	require.NoError(t, err)
	t.Logf("序列化结果: %s", raw)

	body := make(map[string]interface{})
	require.NoError(t, json.Unmarshal(raw, &body))

	if _, exists := body["data"]; exists {
		t.Fatal("data 字段不应该在错误响应中出现")
	}
	if _, exists := body["extensions"]; exists {
		t.Fatal("extensions 字段不应该在顶层响应中出现")
	}

	errorsValue, ok := body["errors"]
	require.True(t, ok, "errors 字段应该存在")

	errorsSlice, ok := errorsValue.([]interface{})
	require.True(t, ok)
	require.Len(t, errorsSlice, 1)

	firstError, ok := errorsSlice[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "请求参数错误", firstError["message"])

	extensions, ok := firstError["extensions"].(map[string]interface{})
	require.True(t, ok)

	if _, exists := extensions["errors"]; exists {
		t.Fatal("extensions 中不应该包含 errors 字段列表")
	}

	usernameMsg, ok := extensions["username"].(string)
	require.True(t, ok)
	require.Equal(t, "用户名为必填字段", usernameMsg)

	ageMsg, ok := extensions["age"].(string)
	require.True(t, ok)
	require.Equal(t, "年龄必须大于或等于18", ageMsg)
}

func TestValidatorGeneralMessageTranslation(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	translator, ok := v.universal.GetTranslator(LocaleChinese)
	require.True(t, ok)

	msg, err := translator.T(generalMessageKey)
	require.NoError(t, err)
	require.Equal(t, "参数校验失败", msg)

	translator, ok = v.universal.GetTranslator(LocaleEnglish)
	require.True(t, ok)

	msg, err = translator.T(generalMessageKey)
	require.NoError(t, err)
	require.Equal(t, "validation failed", msg)
}
