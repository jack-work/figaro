// Package toolout bounds and coalesces streamed tool text. The caller drives
// cadence: no timers, no locks.
package toolout

import "strings"

const defaultMaxLines = 10

type Governor struct {
	maxLines int
	tails    map[string]string
	dirty    bool
}

func New(maxLines int) *Governor {
	if maxLines <= 0 {
		maxLines = defaultMaxLines
	}
	return &Governor{maxLines: maxLines, tails: make(map[string]string)}
}

func (g *Governor) Feed(key, chunk string) {
	if chunk == "" {
		return
	}
	g.tails[key] = clampTail(g.tails[key]+chunk, g.maxLines)
	g.dirty = true
}

func (g *Governor) Tail(key string) string { return g.tails[key] }

// Tails is the live map itself; the caller must not mutate it.
func (g *Governor) Tails() map[string]string { return g.tails }

func (g *Governor) Dirty() bool { return g.dirty }

func (g *Governor) ClearDirty() { g.dirty = false }

func (g *Governor) Drop(key string) { delete(g.tails, key) }

// clampTail keeps the last n lines; a trailing partial line counts as one.
func clampTail(s string, n int) string {
	if s == "" {
		return s
	}
	newlines := strings.Count(s, "\n")
	lineCount := newlines
	if !strings.HasSuffix(s, "\n") {
		lineCount++
	}
	if lineCount <= n {
		return s
	}
	skip := lineCount - n
	idx := 0
	for i := 0; i < skip; i++ {
		j := strings.IndexByte(s[idx:], '\n')
		if j < 0 {
			return ""
		}
		idx += j + 1
	}
	return s[idx:]
}
