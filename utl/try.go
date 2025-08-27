package utl

// result 私有泛型结果类型，支持Must模式
type result[T any] struct {
	value T
	err   error
}

// Must 返回值，如果有错误则panic
func (r result[T]) Must() T {
	if r.err != nil {
		panic(r.err)
	}
	return r.value
}

// Try 泛型封装函数，将 (T, error) 转为 result[T]
func Try[T any](value T, err error) result[T] {
	return result[T]{value: value, err: err}
}

// Must 保证函数返回的错误不会为 nil，否则会 panic
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// If 泛型三目表达式
func If[T any](condition bool, trueValue, falseValue T) T {
	if condition {
		return trueValue
	}
	return falseValue
}

func Safe[T comparable](value T, ok bool, defaultValue T, validators ...func(T) bool) (T, bool) {
	var zero T
	// 先检查基本条件：ok 且不是零值
	if !ok || value == zero {
		return defaultValue, false
	}
	// 再检查自定义验证器
	for _, validator := range validators {
		if !validator(value) {
			return defaultValue, false
		}
	}
	return value, true
}
