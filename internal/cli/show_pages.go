package cli

// The daemon composes turns; this walks its pages until the selector is covered.

import (
	"context"
	"github.com/jack-work/figaro/sdk"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// The daemon clamps this to its own ceiling.
const showPageBytes = 512 << 10

type showTurns struct {
	turns  []aria.Turn
	atHead bool // nothing older than turns[0]
	atTail bool // nothing newer than the last turn
	pages  int
}

func gatherShowTurns(ctx context.Context, acli *sdk.Angelus, figaroID string, opts showOpts) (showTurns, error) {
	var w showTurns
	if opts.from >= 0 {
		return gatherForward(ctx, acli, figaroID, opts)
	}
	var at aria.Anchor
	if opts.before >= 0 {
		at = aria.Anchor{Turn: uint64(opts.before)}
	}
	for {
		page, err := acli.Read(ctx, rpc.ReadRequest{
			FigaroID: figaroID, At: at, Backward: true, Limit: showPageBytes,
		})
		if err != nil {
			return w, err
		}
		w.pages++
		got := joinParts(page)
		if len(got) == 0 {
			w.atHead = true
			return w, nil
		}
		if w.pages == 1 {
			w.atTail = !page.More.After && opts.before < 0
		}
		w.turns = prepend(got, w.turns)
		if !page.More.Before || page.Prev == nil {
			w.atHead = true
			return w, nil
		}
		// A page is cut by a byte budget, so its oldest element is usually
		// mid-turn; an anchor derived from the turn alone would drop the
		// rest of it.
		at = *page.Prev
		if turnsSatisfy(w, opts) {
			return w, nil
		}
	}
}

// prepend welds the turn a page boundary cut in half.
func prepend(older, newer []aria.Turn) []aria.Turn {
	if len(older) == 0 {
		return newer
	}
	if len(newer) > 0 && older[len(older)-1].ID == newer[0].ID {
		seam := older[len(older)-1]
		seam.Nodes = append(append([]livedoc.Node(nil), seam.Nodes...), newer[0].Nodes...)
		older = append(older[:len(older)-1], seam)
		newer = newer[1:]
	}
	return append(older, newer...)
}

// --from names its oldest turn; a backward walk would have to reach the head to
// find it.
func gatherForward(ctx context.Context, acli *sdk.Angelus, figaroID string, opts showOpts) (showTurns, error) {
	var w showTurns
	at := aria.Anchor{Turn: uint64(max(opts.from, 0))}
	for {
		page, err := acli.Read(ctx, rpc.ReadRequest{
			FigaroID: figaroID, At: at, Limit: showPageBytes,
		})
		if err != nil {
			return w, err
		}
		w.pages++
		got := joinParts(page)
		if len(got) == 0 {
			w.atTail = true
			return w, nil
		}
		if w.pages == 1 {
			w.atHead = !page.More.Before
		}
		w.turns = prepend(w.turns, got)
		last := w.turns[len(w.turns)-1]
		if !page.More.After || page.Next == nil {
			w.atTail = true
			return w, nil
		}
		if opts.to >= 0 && last.ID >= uint64(opts.to) {
			return w, nil
		}
		if *page.Next == at {
			return w, nil // no progress
		}
		at = *page.Next
	}
}

// One turn can arrive as several parts (TurnPart.From); the renderer draws turns.
func joinParts(p aria.Page) []aria.Turn {
	var out []aria.Turn
	for _, part := range p.Parts {
		if n := len(out); n > 0 && out[n-1].ID == part.ID && part.From > 0 {
			out[n-1].Nodes = append(out[n-1].Nodes, part.Nodes...)
			continue
		}
		t := part.Turn
		t.Nodes = append([]livedoc.Node(nil), part.Nodes...)
		out = append(out, t)
	}
	return out
}

// Each case asks for one more turn than it will show: the oldest may be the one
// the next page completes.
func turnsSatisfy(w showTurns, opts showOpts) bool {
	if opts.all {
		return w.atHead
	}
	if len(w.turns) == 0 {
		return false
	}
	switch {
	case opts.before >= 0:
		n := 0
		for _, t := range w.turns {
			if t.ID < uint64(opts.before) {
				n++
			}
		}
		return opts.last > 0 && n > opts.last
	default:
		return opts.last > 0 && len(w.turns) > opts.last
	}
}

func selectShowRange(ts []aria.Turn, opts showOpts) (lo, hi int) {
	if len(ts) == 0 {
		return 0, 0
	}
	switch {
	case opts.all:
		return 0, len(ts)
	case opts.from >= 0:
		lo = len(ts)
		hi = len(ts)
		for i, t := range ts {
			if t.ID >= uint64(opts.from) && i < lo {
				lo = i
			}
			if opts.to >= 0 && t.ID > uint64(opts.to) {
				hi = i
				break
			}
		}
		if lo > hi {
			lo = hi
		}
		return lo, hi
	case opts.before >= 0:
		hi = len(ts)
		for i, t := range ts {
			if t.ID >= uint64(opts.before) {
				hi = i
				break
			}
		}
		lo = hi - opts.last
		if lo < 0 {
			lo = 0
		}
		return lo, hi
	default:
		hi = len(ts)
		lo = hi - opts.last
		if lo < 0 {
			lo = 0
		}
		return lo, hi
	}
}
