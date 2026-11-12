// Package anthropicmodels holds the client-version facts shared by the
// Anthropic-shaped providers.
package anthropicmodels

import (
	"strconv"
	"strings"
)

// normalize folds "claude-opus-4.6" and "anthropic/claude-opus-4-6" onto
// "claude-opus-4-6".
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.ReplaceAll(m, ".", "-")
}

// ClaudeCodeVersion is the client version presented to the API, which gates
// newer models on it (claude_code_version_too_old). Track the `latest` npm
// dist-tag, not `stable`: model floors can sit above stable. When a model
// 400s, record its floor in versionFloors and bump this.
const ClaudeCodeVersion = "2.1.322"

// versionFloors holds, per model prefix, the client version a
// claude_code_version_too_old 400 named.
var versionFloors = map[string]string{
	"claude-fable-5-1": "2.1.251",
	"claude-opus-5-5":  "2.1.280",
}

// VersionFloor returns "" when no floor is recorded; longest prefix wins.
func VersionFloor(model string) string {
	m := normalize(model)
	best, floor := 0, ""
	for prefix, v := range versionFloors {
		if len(prefix) <= best || !strings.HasPrefix(m, prefix) {
			continue
		}
		if len(m) > len(prefix) && m[len(prefix)] != '-' {
			continue
		}
		best, floor = len(prefix), v
	}
	return floor
}

// CompareVersions returns -1, 0 or 1; missing components read as zero.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
