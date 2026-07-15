// Package toolout bounds and coalesces streamed tool text (execution output
// and streamed tool-argument previews). It is deterministic and single-
// threaded: the caller drives cadence, this package holds no timers and no
// locks.
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

// Tails exposes the live bounded tails keyed by id for a consumer that reads
// every key at once (composing a frame). Read-only — the caller must not mutate
// the returned map. Zero-copy: safe because the governor is single-threaded.
func (g *Governor) Tails() map[string]string { return g.tails }

func (g *Governor) Dirty() bool { return g.dirty }

func (g *Governor) ClearDirty() { g.dirty = false }

func (g *Governor) Drop(key string) { delete(g.tails, key) }

// clampTail keeps only the last n lines of s. A trailing partial line (no
// terminating '\n') counts as a line.
func clampTail(s string, n int) string {
	if s == "" {
		return s
	}
	// Count '\n'; last-line-with-newline vs partial trailing line.
	newlines := strings.Count(s, "\n")
	lineCount := newlines
	if !strings.HasSuffix(s, "\n") {
		lineCount++
	}
	if lineCount <= n {
		return s
	}
	// Find start of the (lineCount-n)-th line's successor: skip that many '\n'.
	skip := lineCount - n
	// If the tail has a partial trailing line, the "lines" are:
	//   line0 \n line1 \n ... \n lineK-1 \n partial
	// where K = newlines. Skipping `skip` lines means advancing past `skip`
	// newlines from the start.
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
