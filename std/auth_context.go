package std

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
)

// CurrentUserKey 统一在 Fiber 上下文中存放当前用户标识，避免重复定义。
const CurrentUserKey = "__current_user_id__"

const (
	rolePrefix = "r_"
	userPrefix = "u_"
	nodePrefix = "n_"
)

// AuthExtract 从 JWT 负载解析出校验后的登录用户标识，借助二次序列化/反序列化触发 Id 的自定义 JSON 逻辑，从而兼容 sqids 与数值形式。
func AuthExtract(claims jwt.MapClaims) (Id, error) {
	if claims == nil {
		return 0, errors.New("令牌负载为空")
	}
	payload := struct {
		Sub Id `json:"sub"`
	}{}
	if data, err := json.Marshal(claims); err != nil {
		return 0, errors.New("令牌负载序列化失败")
	} else if err = json.Unmarshal(data, &payload); err != nil || payload.Sub == 0 {
		return 0, errors.New("主体标识解析失败")
	}
	return payload.Sub, nil
}

// AuthAttach 将用户标识写入 Fiber locals 与上下文供后续审计、授权使用。
func AuthAttach(c fiber.Ctx, userID Id) error {
	if userID == 0 {
		return fiber.NewError(fiber.StatusUnauthorized, "主体标识无效")
	}
	c.Locals(CurrentUserKey, userID)
	c.SetContext(SetAuditUser(c.Context(), userID))
	return nil
}

// AuthSubject 统一读取 Casbin Subject，必要时按需补充前缀。
func AuthSubject(c fiber.Ctx) string {
	if id := currentUserFromFiber(c); id > 0 {
		return UserKey(id)
	}
	return ""
}

// AuthCurrent 暴露统一的上下文读取能力，避免 API 层重复解析。
func AuthCurrent(c fiber.Ctx) Id {
	return currentUserFromFiber(c)
}

// UserKey 为用户 ID 添加 Casbin 前缀。
func UserKey(id Id) string {
	if id <= 0 {
		return ""
	}
	return userPrefix + strconv.FormatInt(int64(id), 10)
}

// ParseUserKey 解析带前缀的用户标识。
func ParseUserKey(key string) (Id, bool) {
	return parsePrefixedID(userPrefix, key)
}

// RoleKey 按约定为角色 ID 添加前缀。
func RoleKey(id Id) string {
	if id <= 0 {
		return ""
	}
	return rolePrefix + strconv.FormatInt(int64(id), 10)
}

// ParseRoleKey 解析带前缀的角色标识。
func ParseRoleKey(key string) (Id, bool) {
	return parsePrefixedID(rolePrefix, key)
}

// NodeKey 按约定为节点 ID 添加前缀。
func NodeKey(id Id) string {
	if id <= 0 {
		return ""
	}
	return nodePrefix + strconv.FormatInt(int64(id), 10)
}

// ParseNodeKey 解析带前缀的节点标识。
func ParseNodeKey(key string) (Id, bool) {
	return parsePrefixedID(nodePrefix, key)
}

func parsePrefixedID(prefix, key string) (Id, bool) {
	if !strings.HasPrefix(key, prefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(key, prefix)
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return Id(id), true
}

// currentUserFromFiber 读取 Fiber 上下文记录的当前用户。
func currentUserFromFiber(c fiber.Ctx) Id {
	switch val := c.Locals(CurrentUserKey).(type) {
	case Id:
		return val
	case uint64:
		return Id(val)
	case int64:
		if val > 0 {
			return Id(val)
		}
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return 0
		}
		if id, err := strconv.ParseUint(val, 10, 64); err == nil && id > 0 {
			return Id(id)
		}
	}
	return 0
}
