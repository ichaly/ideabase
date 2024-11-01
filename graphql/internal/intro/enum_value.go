package intro

import (
	"encoding/json"
	"github.com/vektah/gqlparser/v2/ast"
)

type __EnumValue struct {
	*ast.EnumValueDefinition
}

func (my __EnumValue) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	if len(my.Name) > 0 {
		res["name"] = my.Name
	}
	if len(my.Description) > 0 {
		res["description"] = my.Description
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
