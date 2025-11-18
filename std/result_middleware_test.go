package std

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestResultMiddlewareSuccess(t *testing.T) {
	app := fiber.New()
	app.Use(ResultMiddleware())

	app.Get("/ok", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"message": "ok"})
	})

	resp := perform(app, http.MethodGet, "/ok")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "", result.Message)

	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "ok", data["message"])
	require.Nil(t, result.Errors)
}

func TestResultMiddlewareError(t *testing.T) {
	app := fiber.New()
	app.Use(ResultMiddleware())

	app.Get("/bad", func(c fiber.Ctx) error {
		return NewException(fiber.StatusBadRequest).WithMessage("bad request")
	})

	resp := perform(app, http.MethodGet, "/bad")
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result Result
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "bad request", result.Message)
	require.Nil(t, result.Data)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "bad request", result.Errors[0].Message)
}

func TestResultMiddlewareSkip(t *testing.T) {
	app := fiber.New()
	app.Use(ResultMiddleware(WithResultSkipper(func(route *fiber.Route) bool {
		return route != nil && route.Path == "/raw"
	})))

	app.Get("/raw", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "raw"})
	})

	resp := perform(app, http.MethodGet, "/raw")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var content map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&content))
	require.Equal(t, "raw", content["status"])
}

func TestResultMiddlewarePanic(t *testing.T) {
	app := fiber.New()
	app.Use(ResultMiddleware())

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
