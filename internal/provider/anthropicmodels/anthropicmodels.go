// Package anthropicmodels holds the context-window knowledge shared by the
// two Anthropic providers (the hand-rolled HTTP one and the SDK one).
package anthropicmodels

import (
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
