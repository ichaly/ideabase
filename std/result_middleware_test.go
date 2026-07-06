package std

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// newEnvelopeApp 复刻 NewFiber 的信封机制（编码器包装 + 统一错误 + panic恢复），单测用
func newEnvelopeApp() *fiber.App {
	app := fiber.New(fiber.Config{
		JSONEncoder:  wrapJSON,
		JSONDecoder:  fiberJSON.Unmarshal,
		ErrorHandler: resultErrorHandler,
	})
	app.Use(recoverMiddleware)
	return app
}

func TestEnvelopeSuccess(t *testing.T) {
	app := newEnvelopeApp()
	app.Get("/ok", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"message": "ok"})
	})

	resp := perform(app, http.MethodGet, "/ok")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, fiber.StatusOK, result.Code)
	require.Equal(t, "", result.Message)

	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "ok", data["message"])
	require.Nil(t, result.Errors)
}

// TestEnvelopePassthrough *Result 原样输出，不再被二次包装（GraphQL 走此路径）
func TestEnvelopePassthrough(t *testing.T) {
	app := newEnvelopeApp()
	app.Get("/gql", func(c fiber.Ctx) error {
		return c.JSON(&Result{Code: fiber.StatusOK, Data: json.RawMessage(`{"botProfiles":{"total":5}}`)})
	})

	resp := perform(app, http.MethodGet, "/gql")
	defer resp.Body.Close()

	var result Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, fiber.StatusOK, result.Code)
	// 单层 data：data 直接是 GraphQL 内容，没有 data.data 嵌套
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, data, "botProfiles")
	require.NotContains(t, data, "data")
}

func TestEnvelopeError(t *testing.T) {
	app := newEnvelopeApp()
	app.Get("/bad", func(c fiber.Ctx) error {
		return NewException(fiber.StatusBadRequest).WithMessage("bad request")
	})

	resp := perform(app, http.MethodGet, "/bad")
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, fiber.StatusBadRequest, result.Code)
	require.Equal(t, "bad request", result.Message)
	require.Nil(t, result.Data)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "bad request", result.Errors[0].Message)
}

func TestEnvelopePanic(t *testing.T) {
	app := newEnvelopeApp()
	app.Get("/panic", func(c fiber.Ctx) error {
		panic("boom")
	})

	resp := perform(app, http.MethodGet, "/panic")
	defer resp.Body.Close()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var result Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "服务器内部错误", result.Message)
	require.Nil(t, result.Data)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "服务器内部错误", result.Errors[0].Message)
}

func perform(app *fiber.App, method, path string) *http.Response {
	req := httptest.NewRequest(method, path, nil)
	resp, err := app.Test(req)
	if err != nil {
		panic(err)
	}
	return resp
}
