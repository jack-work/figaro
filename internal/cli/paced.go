package cli

import (
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/livedoc"
)

// pacedLive wraps a liveRegion with consumer-side delta pacing: incoming
// deltas accumulate into a target blob, and tick() reveals it toward the
// terminal a few runes at a time so prose streams typewriter-style.
// Tail appends (the streaming-text case) are paced; any structural change
// (a spinner flip, a tool fence replacing partial output) snaps through
// immediately. Spinners animate on the same tick.
//
// Not safe for concurrent use; the driver serializes calls (notify pump,
// ticker, SIGWINCH) on one mutex.
type pacedLive struct {
	lr        *liveRegion
	perTick   int // runes revealed per tick (≈ CPS / tickFPS)
	target    string
	shown     string
	spinEvery int
	ticks     int
}

func newPacedLive(lr *liveRegion, cps, tickFPS int) *pacedLive {
	per := cps / tickFPS
	if per < 1 {
		per = 1
	}
	// Spin roughly every ~90ms regardless of the (faster) pacing tick.
	spinEvery := tickFPS / 11
	if spinEvery < 1 {
		spinEvery = 1
	}
	return &pacedLive{lr: lr, perTick: per, spinEvery: spinEvery}
}

// snapshot replaces the unit wholesale (no pacing — a snapshot is full
// state), flushing any pending reveal first.
func (p *pacedLive) snapshot(md string) {
	p.flush()
	p.lr.snapshot(md)
	p.target, p.shown = md, md
}

// queueDelta records a delta against the target; tick() reveals it.
func (p *pacedLive) queueDelta(d livedoc.Delta) {
	p.target = livedoc.Apply(p.target, d)
}

// commit reveals everything pending and freezes the unit.
func (p *pacedLive) commit() {
	p.flush()
	p.lr.commit()
	p.target, p.shown = "", ""
}

// resize forwards to the region (operating on the shown blob).
func (p *pacedLive) resize(width int) { p.lr.resize(width) }

// flush reveals all pending target content at once.
func (p *pacedLive) flush() {
	if d, ok := livedoc.Diff(p.shown, p.target); ok {
		p.lr.applyDelta(d)
		p.shown = p.target
	}
}

// tick reveals up to perTick runes toward the target and advances the
// spinner.
func (p *pacedLive) tick() {
	if p.shown != p.target {
		if d, ok := livedoc.Diff(p.shown, p.target); ok {
			if d.Del == 0 && d.At == len(p.shown) {
				// Pure tail append — reveal a chunk.
				chunk := firstRunes(d.Ins, p.perTick)
				sd := livedoc.Delta{At: len(p.shown), Del: 0, Ins: chunk}
				p.lr.applyDelta(sd)
				p.shown += chunk
			} else {
				// Structural change — snap it through.
				p.lr.applyDelta(d)
				p.shown = p.target
			}
		}
	}
	p.ticks++
	if p.ticks%p.spinEvery == 0 {
		p.lr.tickSpin()
	}
}

// firstRunes returns the first n runes of s (whole string if shorter).
func firstRunes(s string, n int) string {
	i, count := 0, 0
	for i < len(s) && count < n {
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		count++
	}
	return s[:i]
}
