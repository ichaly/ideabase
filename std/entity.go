package std

import (
	"context"
	"strconv"

	"gorm.io/datatypes"
)

var UserContextKey = userContextKeyType{}

type userContextKeyType struct{}

type Id uint64

type Primary struct {
	Id Id `gorm:"primary_key;comment:主键;next:sonyflake;" json:"id,omitempty"`
}

type General struct {
	State     int8              `gorm:"index;comment:状态;default:1" json:"state"`
	Weight    int8              `gorm:"comment:权重;" json:"weight"`
	Remark    datatypes.JSONMap `gorm:"comment:备注" json:"remark,omitempty"`
	CreatedAt *DataTime         `gorm:"index;comment:创建时间;autoCreateTime" json:"createdAt,omitempty"`
	UpdatedAt *DataTime         `gorm:"comment:更新时间;autoUpdateTime" json:"updatedAt,omitempty"`
}

type Entity struct {
	Primary `mapstructure:",squash"`
	General `mapstructure:",squash"`
}

type AuditorEntity struct {
	Entity    `mapstructure:",squash"`
	CreatedBy *Id `gorm:"index;comment:创建人;" json:"createdBy,omitempty"`
	UpdatedBy *Id `gorm:"comment:更新人;" json:"updatedBy,omitempty"`
	DeletedBy *Id `gorm:"comment:删除人;" json:"deletedBy,omitempty"`
}

type DeletedEntity struct {
	AuditorEntity `mapstructure:",squash"`
	DeletedAt     FlagTime `gorm:"index;comment:逻辑删除;" json:"deletedAt,omitempty"`
}

// Encode 将内部数字 ID 编码为可在外部场景（如 JWT）稳定传输的字符串。
// 这里使用 shortId 字符串，避免跨语言 number/float64 精度丢失。
func (my Id) Encode() string {
	if my == 0 {
		return ""
	}
	if str, err := shortId.Encode([]uint64{uint64(my)}); err == nil {
		return str
	}
	return strconv.FormatUint(uint64(my), 10)
}

// Decode 将外部传入的 token（十进制字符串或 shortId）解析回内部数字 ID。
func (my *Id) Decode(token string) error {
	id, err := parseIdToken(token)
	if err != nil {
		return err
	}
	*my = id
	return nil
}

func GetAuditUser(ctx context.Context) Id {
	if ctx == nil {
		return 0
	}
	if id, ok := ctx.Value(UserContextKey).(Id); ok {
		return id
	}
	return 0
}

// SetAuditUser 将用户 ID 写入上下文，供审计插件获取当前操作人
func SetAuditUser(ctx context.Context, id Id) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == 0 {
		return ctx
	}
	return context.WithValue(ctx, UserContextKey, id)
}
