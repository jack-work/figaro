// Package anthropicmodels holds the context-window knowledge shared by the
// two Anthropic providers (the hand-rolled HTTP one and the SDK one).
//
// Two sources, in priority order:
//
//  1. a Catalog learned from the provider's own /v1/models endpoint, whose
//     ModelInfo carries max_input_tokens — authoritative, but only populated
//     once something has listed models in this process;
//  2. a static prefix table for the models we have verified, so a fresh
//     process still reports a window.
//
// Unrecognised models return 0 — "unknown". A missing number is honest; a
// wrong one is not.
package anthropicmodels

import (
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/provider"
)

// windowTable maps a normalized model-id prefix to that model's total context
// window in tokens. Longest matching prefix wins, so "claude-opus-4-6" beats
// "claude-opus-4".
//
// Verified facts only:
//   - Opus 5, Opus 4.6/4.7/4.8, Sonnet 5 and Sonnet 4.6 are 1M-token windows.
//     For Opus 5, 1M is both the default and the maximum: there is no smaller
//     variant and no beta header is involved (the context-1m-2025-08-07 beta
//     was retired in April 2026 and is ignored).
//   - Opus 4.5 and earlier, Sonnet 4.5 and earlier, Haiku 4.5 and earlier,
//     and the whole Claude 3 family are 200k.
//
// Anything not listed here is deliberately absent rather than guessed.
var windowTable = map[string]int{
	"claude-opus-5":     1_000_000,
	"claude-opus-4-8":   1_000_000,
	"claude-opus-4-7":   1_000_000,
	"claude-opus-4-6":   1_000_000,
	"claude-sonnet-5":   1_000_000,
	"claude-sonnet-4-6": 1_000_000,

	"claude-opus-4":   200_000,
	"claude-opus-4-1": 200_000,
	"claude-opus-4-5": 200_000,
	"claude-sonnet-4": 200_000,
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
// chalkboard wins outright (that is what an override is for), otherwise the
// catalog/table window is reported. It never performs network I/O — callers
// use it on live status paths.
func (c *Catalog) ContextLimit(model string, snapshot chalkboard.Snapshot) int {
	if override, ok := provider.ContextLimitOverride(snapshot); ok {
		return override
	}
	return c.Window(model)
}
