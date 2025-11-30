package utl

import (
	"path"
	"strings"
)

// NormalizePath 统一规范化路由路径，去除多余斜杠并确保以 / 开头。
func NormalizePath(raw string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(raw))
	if cleaned == "." {
		return "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		return "/" + cleaned
	}
	return cleaned
}
