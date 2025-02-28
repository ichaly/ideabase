package intro

import (
	"encoding/json"
	"github.com/vektah/gqlparser/v2/ast"
	"strings"
)

type __Field struct {
	*ast.FieldDefinition
}

func (my __Field) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	res["name"] = my.Name
	if len(my.Description) > 0 {
		res["description"] = my.Description
	}
	if !strings.HasPrefix(my.Type.Name(), "__") {
		res["type"] = &__Type{my.Type}
	}

	//必须存在不能为nil
	args := make([]__InputValue, 0, len(my.Arguments))
	for _, a := range my.Arguments {
		args = append(args, __InputValue{a})
	}
	res["args"] = args

	directive := my.Directives.ForName("deprecated")
	res["isDeprecated"] = directive != nil
	if directive != nil {
		reason := directive.Arguments.ForName("reason")
		if reason != nil && reason.Value != nil {
			res["deprecationReason"] = reason.Value.Raw
		}
	}

	return json.Marshal(res)
}
