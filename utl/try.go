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
