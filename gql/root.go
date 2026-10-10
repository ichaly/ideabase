package gql

import (
	"fmt"
	"reflect"
	"strconv"
	"sync"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Root 是根 Resolver 的请求级执行能力。I 为完整参数，O 为返回值。
// Next 仅调用当前字段的原生实现一次，并沿用当前上下文、权限和事务。
type Root[I, O any] struct{ call *rootCall }

func (my Root[I, O]) withRoot(call *rootCall) any { return Root[I, O]{call} }
func (my Root[I, O]) rootTypes() (reflect.Type, reflect.Type) {
	return reflect.TypeFor[I](), reflect.TypeFor[O]()
}

// Has 使用 GraphQL 参数路径，例如 input.name、input[0].name；null 也算提交。
func (my Root[I, O]) Has(path string) bool {
	if my.call == nil {
		return false
	}
	if my.call.native == nil {
		return my.call.present[path]
	}
	state := my.call.native.input.lookup(path, &my.call.input, false)
	return state != nil && state.present
}

// Include 显式加入省略的字段；未知路径会使整个 Resolver 失败。
func (my Root[I, O]) Include(paths ...string) {
	if my.call == nil {
		return
	}
	for _, path := range paths {
		if my.call.native == nil || my.call.native.input.lookup(path, &my.call.input, true) == nil {
			my.call.err = fmt.Errorf("resolver未绑定参数路径: %s", path)
			return
		}
	}
}

func (my Root[I, O]) Next(input I) (out O, err error) {
	if my.call == nil {
		return out, fmt.Errorf("Next只能在Resolver执行期间使用")
	}
	call := my.call
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.closed || call.called || call.next == nil {
		call.err = &publicFailure{fmt.Sprintf("%s没有可继续执行的原生Resolver，或Next已调用/执行已结束", call.coordinate), "RESOLVER_NEXT_UNAVAILABLE"}
		return out, call.err
	}
	call.called = true
	if call.err != nil {
		return out, call.err
	}
	value, err := call.next(&input)
	if err == nil && value != nil {
		err = call.native.output.decode(reflect.ValueOf(&out).Elem(), value, nil)
	}
	call.err = err
	return out, err
}

type rootFactory interface {
	withRoot(*rootCall) any
	rootTypes() (reflect.Type, reflect.Type)
}

func argumentPaths(value any, path string, present map[string]bool) {
	if path != "" {
		present[path] = true
	}
	switch value := value.(type) {
	case map[string]any:
		for name, child := range value {
			argumentPaths(child, inputPath(path, name), present)
		}
	case []any:
		for i, child := range value {
			argumentPaths(child, path+"["+strconv.Itoa(i)+"]", present)
		}
	}
}

type rootCall struct {
	coordinate     string
	native         *nativeResolver
	present        map[string]bool
	input          inputState
	next           func(any) (any, error)
	mu             sync.Mutex
	called, closed bool
	err            error
	result         any
	warnings       gqlerror.List
	finish         func(any, any) (any, error)
}
