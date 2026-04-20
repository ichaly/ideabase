package std

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type txKey struct{}

// BaseService 提供通用的事务封装能力：根据 ctx 自动取事务连接或普通连接，
// 并暴露 WithTransaction 开启事务。
type BaseService struct {
	root *gorm.DB
}

// NewBaseService 构造基础服务；db 为 nil 直接 panic（属编程错误）。
func NewBaseService(db *gorm.DB) *BaseService {
	if db == nil {
		panic("BaseService: 数据库连接不能为空")
	}
	return &BaseService{root: db}
}

// DB 取当前上下文的数据库连接：ctx 中有事务则返回事务连接，否则返回绑定 ctx 的普通连接。
func (my *BaseService) DB(ctx context.Context) *gorm.DB {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx
	}
	return my.root.WithContext(ctx)
}

// WithTransaction 在事务中执行 fn；fn 拿到的 ctx 已携带事务句柄，下游 DB(ctx) 会自动沿用。
func (my *BaseService) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	if my == nil || my.root == nil {
		return errors.New("基础服务未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return my.root.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}
