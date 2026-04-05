package providers

import "testing"

func TestMatchTopic(t *testing.T) {
	tests := []struct {
		pattern, topic string
		want           bool
	}{
		{"cms:content:like", "cms:content:like", true},
		{"cms:content:*", "cms:content:like", true},
		{"cms:content:*", "cms:content:star", true},
		{"cms:content:*", "cms:comment:create", false},
		{"cms:content:*", "cms:content:sub:deep", false},
		{"cms:*:like", "cms:content:like", true},
		{"cms:*:like", "cms:comment:like", true},
		{"cms:*:like", "cms:content:star", false},
		{"*:*:*", "a:b:c", true},
		{"*:*:*", "a:b", false},
		{"test", "test", true},
		{"test", "other", false},
	}
	for _, tt := range tests {
		got := MatchTopic(tt.pattern, tt.topic)
		if got != tt.want {
			t.Errorf("MatchTopic(%q, %q) = %v, want %v", tt.pattern, tt.topic, got, tt.want)
		}
	}
}
