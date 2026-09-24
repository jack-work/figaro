// Package anthropicmodels holds the context-window knowledge shared by the
// two Anthropic providers (the hand-rolled HTTP one and the SDK one).
package anthropicmodels

import (
	"strconv"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
)

// windowTable maps a normalized model-id prefix to that model's total context
// window in tokens. Longest matching prefix wins, so "claude-opus-4-6" beats
// "claude-opus-4".
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

// normalize folds the spellings we see across surfaces onto one form: the
// Anthropic API uses "claude-opus-4-6", Copilot's catalog uses
// "claude-opus-4.6", and some configs prefix a vendor ("anthropic/...").
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.ReplaceAll(m, ".", "-")
}

// ContextWindow returns the static table's window for model, or 0 if the model
// is not one we have verified.
func ContextWindow(model string) int {
	m := normalize(model)
	best, window := 0, 0
	for prefix, w := range windowTable {
		if len(prefix) <= best || !strings.HasPrefix(m, prefix) {
			continue
		}
		// Only match on an id boundary: "claude-opus-4" must not match
		// "claude-opus-42".
		if len(m) > len(prefix) && m[len(prefix)] != '-' {
			continue
		}
		best, window = len(prefix), w
	}
	return window
}

// Catalog caches windows learned from the models endpoint. The zero value is
// ready to use and safe for concurrent access.
type Catalog struct {
	mu      sync.RWMutex
	windows map[string]int
}

// Learn records a model's context window as reported by the provider.
// Non-positive windows are ignored.
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

// Window returns the learned window for model, falling back to the static
// table, then 0.
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

// ContextLimit implements provider.ContextLimitProvider's contract for an
// Anthropic-shaped provider: an explicit system.max_context_tokens on the
// form wins outright (that is what an override is for), otherwise the
// catalog/table window is reported. It never performs network I/O: callers
// use it on live status paths.
func (c *Catalog) ContextLimit(model string, snapshot form.Snapshot) int {
	if override, ok := provider.ContextLimitOverride(snapshot); ok {
		return override
	}
	return c.Window(model)
}

// ClaudeCodeVersion is the client version figaro presents to the Anthropic
// API. It lives HERE, once, because it used to be two constants in two
// providers -- and the API gates newer models on it: Fable 5.1 refuses any
// client below 2.1.251 with a 400 (claude_code_version_too_old) telling you
// to run `claude update`. A stale copy in one provider is an error that
// only appears for whichever models shipped after it, which reads as
// "figaro cannot do fable" when the truth is "one string is old".
//
// TRACK `latest`, NOT `stable`. The floor for Fable 5.1 (2.1.251) is ABOVE
// the stable channel (2.1.236 at the time): real Claude Code on stable
// fails identically (anthropics/claude-code#91345). The check is a numeric
// threshold, not a whitelist of released versions, so the rule when a new
// model 400s is: `npm view @anthropic-ai/claude-code dist-tags.latest`,
// paste it here.
//
// See docs/anthropic-oauth-posture.md for how long this arrangement can be
// expected to hold at all.
const ClaudeCodeVersion = "2.1.267"

// versionFloors records, per model prefix, the client version the API
// demanded in a claude_code_version_too_old 400. Each entry is a fact copied
// out of an error message, not a guess. TestClaudeCodeVersionClearsFloors
// holds ClaudeCodeVersion at or above every one of them, so the next model
// that raises the floor is a red test and a table entry, not a live 400
// found by whoever tries the model first.
var versionFloors = map[string]string{
	"claude-fable-5-1": "2.1.251",
	"claude-opus-5-5":  "2.1.280",
}

// VersionFloor returns the client version the API is known to require for
// model, or "" if no floor has been recorded. Longest matching prefix wins,
// on an id boundary, the same rule ContextWindow uses.
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

// CompareVersions orders two dotted numeric versions ("2.1.280"). Missing
// components read as zero, so "2.1" == "2.1.0". Returns -1, 0 or 1.
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
