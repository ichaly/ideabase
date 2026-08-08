package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type HandlerAddress struct {
	City string `json:"city" validate:"required" doc:"城市"`
}

type HandlerNestedArgs struct {
	Address HandlerAddress   `json:"address" validate:"required"`
	History []HandlerAddress `json:"history"`
}

type HandlerProfile struct {
	Label string `json:"label"`
}

type HandlerPayload struct {
	Address HandlerAddress   `json:"address"`
	Profile *HandlerProfile  `json:"profile"`
	Items   []HandlerProfile `json:"items"`
}

func TestHandlerDerivesNestedInputAndOutputTypes(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	require.NoError(t, executor.Register(NewResolver(
		"Query", "typedHandler", "typed handler",
		func(_ context.Context, _ Root, input HandlerNestedArgs) (HandlerPayload, error) {
			return HandlerPayload{
				Address: input.Address,
				Profile: &HandlerProfile{Label: input.Address.City},
				Items:   []HandlerProfile{{Label: "one"}},
			}, nil
		},
	)))

	addressInput := executor.schema.Types["HandlerAddressInput"]
	require.NotNil(t, addressInput)
	require.True(t, addressInput.IsInputType())
	require.Equal(t, "String!", addressInput.Fields.ForName("city").Type.String())
	require.NotNil(t, executor.schema.Types["HandlerPayload"])
	require.NotNil(t, executor.schema.Types["HandlerProfile"])
	require.Equal(t, "HandlerAddressInput!", executor.schema.Query.Fields.ForName("typedHandler").Arguments.ForName("address").Type.String())

	reply := executor.run(context.Background(), `{
		typedHandler(address: {city: "Shanghai"}, history: [{city: "Hangzhou"}]) {
			address { city }
			profile { label }
			items { label }
		}
	}`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, map[string]any{
		"address": map[string]any{"city": "Shanghai"},
		"profile": map[string]any{"label": "Shanghai"},
		"items":   []any{map[string]any{"label": "one"}},
	}, reply.Data["typedHandler"])
}
