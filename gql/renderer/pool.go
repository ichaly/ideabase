package renderer

import (
	"sync"
)

var fieldPool = sync.Pool{
	New: func() interface{} {
		return &Field{
			Indent: 2,
			Args:   make([]Argument, 0, 4),
		}
	},
}

// getFromPool 从对象池获取Field
func getFromPool() *Field {
	return fieldPool.Get().(*Field)
}

// Release 将Field归还对象池
func Release(f *Field) {
	// 重置字段状态
	f.Name = ""
	f.Type.Name = ""
	f.Type.IsNonNull = false
	f.Type.IsList = false
	f.Type.ListItemNonNull = false
	f.Comment = ""
	f.Indent = 2
	f.Multiline = false
	f.Args = f.Args[:0]

	fieldPool.Put(f)
}
