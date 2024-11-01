package intro

import (
	"encoding/json"
	"github.com/vektah/gqlparser/v2/ast"
)

type __InputValue struct {
	*ast.ArgumentDefinition
}

func (my __InputValue) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	if len(my.Name) > 0 {
		res["name"] = my.Name
	}
	if len(my.Description) > 0 {
		res["description"] = my.Description
	}
	if my.Type != nil {
		res["type"] = __Type{my.Type}
	}
	if my.DefaultValue != nil {
		res["defaultValue"] = my.DefaultValue.String()
	}
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
