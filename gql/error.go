package gql

import "github.com/vektah/gqlparser/v2/gqlerror"

type errorCoder interface{ ErrorCode() string }

// gqlErrors preserves parser/coercion error lists and their public location/path
// instead of wrapping them into one opaque execution error.
func gqlErrors(err error) gqlerror.List {
	if err == nil {
		return nil
	}
	if list, ok := err.(gqlerror.List); ok {
		return list
	}
	if item, ok := err.(*gqlerror.Error); ok {
		return gqlerror.List{item}
	}
	item := gqlerror.Wrap(err)
	if coded, ok := err.(errorCoder); ok {
		item.Extensions = map[string]any{"code": coded.ErrorCode()}
	}
	return gqlerror.List{item}
}
