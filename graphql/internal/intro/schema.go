package intro

import (
	"encoding/json"
	"github.com/vektah/gqlparser/v2/ast"
	"strings"
)

type __Schema struct {
	s *ast.Schema
}

func New(s *ast.Schema) interface{} {
	return map[string]any{"__schema": __Schema{s: s}}
}

func (my __Schema) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	if len(my.s.Types) > 0 {
		types := make([]__FullType, 0, len(my.s.Types))
		for k, v := range my.s.Types {
			if !strings.HasPrefix(k, "__") {
				types = append(types, __FullType{s: my.s, d: v})
			}
		}
		res["types"] = types
	}
	if my.s.Query != nil {
		res["queryType"] = __RootType{my.s.Query}
	}
	if my.s.Mutation != nil {
		res["mutationType"] = __RootType{my.s.Mutation}
	}
	if my.s.Subscription != nil {
		res["subscriptionType"] = __RootType{my.s.Subscription}
	}
	if len(my.s.Directives) > 0 {
		directives := make([]__Directive, 0, len(my.s.Directives))
		for _, d := range my.s.Directives {
			directives = append(directives, __Directive{d})
		}
		res["directives"] = directives
	}
	if len(my.s.Description) > 0 {
		res["description"] = my.s.Description
	}

	return json.Marshal(res)
}
