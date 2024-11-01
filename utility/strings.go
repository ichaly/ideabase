package utility

import (
	"strings"
)

func JoinString(elem ...string) string {
	b := strings.Builder{}
	for _, e := range elem {
		b.WriteString(e)
	}
	return b.String()
}

func StartWithAny(s string, list ...string) (string, bool) {
	for _, p := range list {
		if strings.HasPrefix(s, p) {
			return p, true
		}
	}
	return "", false
}
