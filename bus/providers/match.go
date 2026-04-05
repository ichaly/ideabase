package providers

import "strings"

// MatchTopic 检查发布的 topic 是否匹配订阅 pattern
// `*` 匹配恰好一个段（冒号分隔）
func MatchTopic(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	pp := strings.Split(pattern, ":")
	tp := strings.Split(topic, ":")
	if len(pp) != len(tp) {
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != tp[i] {
			return false
		}
	}
	return true
}
