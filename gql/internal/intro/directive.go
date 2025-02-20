package intro

import (
	"encoding/json"
	"github.com/vektah/gqlparser/v2/ast"
)

type __Directive struct {
	*ast.DirectiveDefinition
}

func (my __Directive) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	if len(my.Name) > 0 {
		res["name"] = my.Name
	}
	if len(my.Description) > 0 {
		res["description"] = my.Description
	}
	if len(my.Locations) > 0 {
		res["locations"] = my.Locations
	}
	res["isRepeatable"] = my.IsRepeatable
	args := make([]__InputValue, 0, len(my.Arguments))
	for _, a := range my.Arguments {
		args = append(args, __InputValue{a})
	}
	res["args"] = args

	return json.Marshal(res)
}
