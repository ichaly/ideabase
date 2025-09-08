package std

import "context"

// Lifecycle 生命周期接口
type Lifecycle interface {
	Append(start, stop func(context.Context) error)
}