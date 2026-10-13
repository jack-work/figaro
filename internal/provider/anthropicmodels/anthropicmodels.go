// Package anthropicmodels holds context-window and client-version facts
// shared by the Anthropic-shaped providers.
package anthropicmodels

import (
	"strconv"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
)

// windowTable is keyed by normalized model-id prefix; longest match wins.
var windowTable = map[string]int{
	"claude-opus-5":     1_000_000,
	"claude-opus-4-8":   1_000_000,
	"claude-opus-4-7":   1_000_000,
	"claude-opus-4-6":   1_000_000,
	"claude-sonnet-5":   1_000_000,
	"claude-sonnet-4-6": 1_000_000,
	"claude-sonnet-4-5": 1_000_000,
	"claude-fable-5":    1_000_000,

	"claude-opus-4":   200_000,
	"claude-opus-4-1": 200_000,
	"claude-opus-4-5": 200_000,
	"claude-haiku-4":  200_000,
	"claude-3":        200_000,
}

// normalize folds "claude-opus-4.6" and "anthropic/claude-opus-4-6" onto
// "claude-opus-4-6".
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.ReplaceAll(m, ".", "-")
}

// ContextWindow returns 0 for a model not in the table.
func ContextWindow(model string) int {
	m := normalize(model)
	best, window := 0, 0
	for prefix, w := range windowTable {
		if len(prefix) <= best || !strings.HasPrefix(m, prefix) {
			continue
		}
		// "claude-opus-4" must not match "claude-opus-42".
		if len(m) > len(prefix) && m[len(prefix)] != '-' {
			continue
		}
		best, window = len(prefix), w
	}
	return window
}

// Catalog's zero value is ready to use and safe for concurrent access.
type Catalog struct {
	mu      sync.RWMutex
	windows map[string]int
}

// Learn ignores non-positive windows.
func (c *Catalog) Learn(model string, window int) {
	if model == "" || window <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.windows == nil {
		c.windows = map[string]int{}
	}
	c.windows[normalize(model)] = window
}

// Window prefers the learned window, then the table, then 0.
func (c *Catalog) Window(model string) int {
	if c != nil {
		c.mu.RLock()
		w, ok := c.windows[normalize(model)]
		c.mu.RUnlock()
		if ok {
			return w
		}
	}
	return ContextWindow(model)
}

// ContextLimit lets system.max_context_tokens win outright. No network I/O.
func (c *Catalog) ContextLimit(model string, snapshot form.Snapshot) int {
	if override, ok := provider.ContextLimitOverride(snapshot); ok {
		return override
	}
	return c.Window(model)
}

// ClaudeCodeVersion is the client version presented to the API, which gates
// newer models on it (claude_code_version_too_old). Track the `latest` npm
// dist-tag, not `stable`: model floors can sit above stable. When a model
// 400s, record its floor in versionFloors and bump this.
const ClaudeCodeVersion = "2.1.281"

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
