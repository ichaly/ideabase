package std

import (
	"time"

	"github.com/invzhi/next"
	"github.com/sony/sonyflake"
	"gorm.io/gorm"
)

var sf *sonyflake.Sonyflake

func init() {
	t, _ := time.Parse("2006-01-02", "2023-07-24")
	sf = sonyflake.NewSonyflake(sonyflake.Settings{StartTime: t})
	if sf == nil {
		panic("sonyflake not created")
	}
}

func NewSonyFlake() gorm.Plugin {
	plugin := next.NewPlugin()
	plugin.Register("sonyflake", func(_, zero bool) (interface{}, error) {
		if !zero {
			return nil, next.SkipField
		}
		return sf.NextID()
	})
	return plugin
}
