package intro

import (
	"encoding/json"
	"github.com/vektah/gqlparser/v2/ast"
	"strings"
)

type __FullType struct {
	s *ast.Schema
	d *ast.Definition
}

func (my __FullType) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	res["name"] = my.d.Name
	res["kind"] = my.d.Kind
	if len(my.d.Description) > 0 {
		res["description"] = my.d.Description
	}
	if len(my.d.Fields) > 0 {
		if my.d.Kind == ast.InputObject {
			inputFields := make([]__InputValue, 0, len(my.d.Fields))
			for _, f := range my.d.Fields {
				inputFields = append(inputFields, __InputValue{&ast.ArgumentDefinition{
					Type:         f.Type,
					Name:         f.Name,
					Description:  f.Description,
					DefaultValue: f.DefaultValue,
					Directives:   f.Directives,
				}})
			}
			res["inputFields"] = inputFields
		} else {
			fields := make([]__Field, 0, len(my.d.Fields))
			for _, f := range my.d.Fields {
				if !strings.HasPrefix(f.Name, "__") {
					fields = append(fields, __Field{f})
				}
			}
			res["fields"] = fields
		}
	}
	if my.d.Kind == ast.Object || my.d.Kind == ast.Interface {
		//如果是Object,必须存在不能为nil
		interfaces := make([]__Type, 0, len(my.d.Interfaces))
		for _, v := range my.d.Interfaces {
			interfaces = append(interfaces, __Type{&ast.Type{NamedType: v}})
		}
		res["interfaces"] = interfaces
	}

	if my.d.Kind == ast.Interface || my.d.Kind == ast.Union {
		possibleTypes := make([]__Type, 0, len(my.s.GetPossibleTypes(my.d)))
		for _, p := range my.s.GetPossibleTypes(my.d) {
			possibleTypes = append(possibleTypes, __Type{&ast.Type{NamedType: p.Name}})
		}
		res["possibleTypes"] = possibleTypes
	}

	if my.d.Kind == ast.Scalar {
		directive := my.d.Directives.ForName("specifiedBy")
		if directive != nil {
			url := directive.Arguments.ForName("url")
			if url != nil && url.Value != nil {
				res["specifiedByURL"] = url.Value.Raw
			}
		}
	}

	if len(my.d.EnumValues) > 0 {
		enumValues := make([]__EnumValue, 0, len(my.d.EnumValues))
		for _, e := range my.d.EnumValues {
			enumValues = append(enumValues, __EnumValue{e})
		}
		res["enumValues"] = enumValues
	}

	return json.Marshal(res)
}

type __RootType struct {
	*ast.Definition
}

func (my __RootType) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})
	res["name"] = my.Name
	return json.Marshal(res)
}

type __Type struct {
	*ast.Type
}

func (my __Type) MarshalJSON() ([]byte, error) {
	res := make(map[string]interface{})

	if !my.NonNull && len(my.NamedType) > 0 {
		res["name"] = my.NamedType
	}
	if my.NonNull {
		res["kind"] = "NON_NULL"
		if my.Elem == nil {
			res["ofType"] = &__Type{&ast.Type{NamedType: my.NamedType}}
		} else {
			res["ofType"] = &__Type{&ast.Type{Elem: my.Elem}}
		}
	} else if my.Elem != nil {
		res["kind"] = "LIST"
		res["ofType"] = &__Type{my.Elem}
	}

	return json.Marshal(res)
}
