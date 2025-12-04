package std

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type txKey struct{}

// BaseService 提供通用的事务封装能力。
type BaseService[T any] struct {
	root  *gorm.DB
	owner T
}

// NewBaseService 绑定宿主服务指针，后续自动完成事务态克隆。
func NewBaseService[T any](db *gorm.DB, owner T) *BaseService[T] {
	if db == nil {
		panic("BaseService: 数据库连接不能为空")
	}
	return &BaseService[T]{root: db, owner: owner}
}

// DB 获取当前上下文的数据库连接（事务或普通连接）。
func (my *BaseService[T]) DB(ctx context.Context) *gorm.DB {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx // 事务中的连接（已经绑定了context）
	}
	return my.root.WithContext(ctx) // 普通连接需要绑定context以支持超时
}

// WithTransaction 执行事务；fn 为事务回调函数，接收事务上下文。
func (my *BaseService[T]) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	if my == nil {
		return errors.New("基础服务未初始化")
	}
	if my.root == nil {
		return errors.New("事务存储未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	return my.root.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, txKey{}, tx)
		return fn(txCtx)
	})
}
