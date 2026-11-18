package store

import (
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/tokens"
)

// HeldThrough is what a fold of view through cut holds in the message and
// turn counts and the context, moved there from base, a sidecar folded
// through base.LastFigaroLT. It reads only the records between the two, or
// from LT 1 when that is shorter, and back from cut to the last usage for the
// context. Token sums come back zero: a sidecar born at an interior fork does
// not hold them as a fold. ok is false when base has no watermark.
func HeldThrough(view arialog.Log[message.Message], base AriaMeta, cut uint64) (AriaMeta, bool) {
	if base.LastFigaroLT == 0 {
		return AriaMeta{}, false
	}
	var held AriaMeta
	switch {
	case cut >= base.LastFigaroLT:
		held.MessageCount, held.TurnCount = base.MessageCount, base.TurnCount
		held.ContextTokens, held.ContextExact = base.ContextTokens, base.ContextExact
		held.LastFigaroLT = base.LastFigaroLT
		for _, e := range readBetween(view, base.LastFigaroLT, cut) {
			held.Fold(e)
		}
	case cut < base.LastFigaroLT-cut:
		for _, e := range readBetween(view, 0, cut) {
			held.Fold(e)
		}
	default:
		held.MessageCount, held.TurnCount = base.MessageCount, base.TurnCount
		for _, e := range readBetween(view, cut, base.LastFigaroLT) {
			if !message.IsCeremonial(e.Payload) {
				held.MessageCount--
			}
			if e.Payload.Role == message.RoleOutput {
				held.TurnCount--
			}
		}
		held.ContextTokens, held.ContextExact = contextThrough(view, cut)
		if prev, _ := view.ReadPage(0, cut+1, 1); len(prev) > 0 {
			held.LastFigaroLT = prev[0].LT
		}
	}
	held.TokensIn, held.TokensOut, held.CacheReadTokens, held.CacheWriteTokens = 0, 0, 0, 0
	return held, true
}

// readBetween is every record with from < LT <= to.
func readBetween(view arialog.Log[message.Message], from, to uint64) []arialog.Entry[message.Message] {
	if to <= from {
		return nil
	}
	var out []arialog.Entry[message.Message]
	for _, e := range view.ReadFrom(from+1, int(to-from)) {
		if e.LT > from && e.LT <= to {
			out = append(out, e)
		}
	}
	return out
}

// contextThrough is the context Fold leaves at cut: the last usage at or
// below it, plus an estimate of every record after that usage.
func contextThrough(view arialog.Log[message.Message], cut uint64) (int, bool) {
	estimate, last := 0, true
	for before := cut + 1; before > 1; {
		page, _ := view.ReadPage(0, before, 64)
		if len(page) == 0 {
			break
		}
		for i := len(page) - 1; i >= 0; i-- {
			m := page[i].Payload
			if m.Usage != nil {
				return tokens.ContextFromUsage(m.Usage) + estimate, last
			}
			estimate += tokens.EstimateMessage(m)
			last = false
		}
		before = page[0].LT
	}
	return estimate, false
}
