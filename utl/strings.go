package utl

import (
	"strings"
)

// JoinString 连接多个字符串
func JoinString(elem ...string) string {
	if len(elem) == 0 {
		return ""
	}

	// 预计算总长度以优化内存分配
	totalLen := 0
	for _, e := range elem {
		totalLen += len(e)
	}

	b := strings.Builder{}
	b.Grow(totalLen)
	for _, e := range elem {
		b.WriteString(e)
	}
	return b.String()
}

// StartWithAny 检查字符串是否以给定的任一前缀开始
func StartWithAny(s string, list ...string) (string, bool) {
	if len(list) == 0 || s == "" {
		return "", false
	}

	// 对于小规模列表，直接遍历
	if len(list) < 10 {
		for _, p := range list {
			if strings.HasPrefix(s, p) {
				return p, true
			}
		}
		return "", false
	}

	// 对于大规模列表，先按长度排序，从最长的开始匹配
	// 这样可以避免短前缀匹配导致的错误结果
	prefixMap := make(map[int][]string)
	maxLen := 0
	for _, p := range list {
		pLen := len(p)
		if pLen > maxLen {
			maxLen = pLen
		}
		prefixMap[pLen] = append(prefixMap[pLen], p)
	}

	// 从最长的前缀开始匹配
	for l := maxLen; l > 0; l-- {
		if prefixes, ok := prefixMap[l]; ok {
			for _, p := range prefixes {
				if strings.HasPrefix(s, p) {
					return p, true
				}
			}
		}
	}

	return "", false
}
