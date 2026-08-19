package cli

// `show`'s page walk. The DAEMON composes turns (it owns the IR, the
// projection and the composed cache); this walks its pages backward or
// forward until the selector is covered and hands the renderer turns.

import (
	"context"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
)

// showPageBytes is one page's byte budget. The daemon clamps it to its own
// ceiling; asking for less only costs round trips.
const showPageBytes = 512 << 10

// showTurns is what the walk holds: whole turns, oldest first, plus
// whether anything exists on either side of them.
type showTurns struct {
	turns  []aria.Turn
	atHead bool // nothing older than turns[0]
	atTail bool // nothing newer than the last turn
	pages  int
}

// gatherShowTurns pulls composed pages until opts is satisfiable.
func gatherShowTurns(ctx context.Context, acli *angelus.Client, figaroID string, opts showOpts) (showTurns, error) {
	var w showTurns
	if opts.from >= 0 {
		return gatherForward(ctx, acli, figaroID, opts)
	}
	before, beforeNode := 0, 0
	if opts.before >= 0 {
		before = opts.before
	}
	for {
		page, err := acli.AriaPage(ctx, rpc.AriaPageRequest{
			FigaroID: figaroID, Before: before, BeforeNode: beforeNode,
			Backward: true, Limit: showPageBytes,
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
		// THE ANCHOR IS (TURN, NODE). A page is cut by a byte budget, so the
		// oldest thing it holds is usually the MIDDLE of a turn -- and a
		// backward read anchored on the turn alone asks for everything before
		// its node 0, silently dropping the rest of that turn. Measured on a
		// real aria: a 143-node turn came back with 9.
		oldest := page.Parts[0]
		before, beforeNode = int(oldest.ID), int(oldest.From)
		w.turns = prepend(got, w.turns)
		if !page.More.Before {
			w.atHead = true
			return w, nil
		}
		if turnsSatisfy(w, opts) {
			return w, nil
		}
	}
}

// prepend joins an OLDER run of turns onto a newer one, welding the turn
// the page boundary cut in half back together.
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

// gatherForward walks up from --from, since that selector names its OLDEST
// turn and a backward walk would have to reach the head to find it.
func gatherForward(ctx context.Context, acli *angelus.Client, figaroID string, opts showOpts) (showTurns, error) {
	var w showTurns
	since := opts.from
	for {
		page, err := acli.AriaPage(ctx, rpc.AriaPageRequest{
			FigaroID: figaroID, SinceLT: since, Limit: showPageBytes,
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
		if !page.More.After {
			w.atTail = true
			return w, nil
		}
		if opts.to >= 0 && last.ID >= uint64(opts.to) {
			return w, nil
		}
		if int(last.ID) <= since {
			return w, nil // no progress; the daemon has nothing further
		}
		since = int(last.ID) + 1
	}
}

// joinParts folds a page's parts into whole turns. A page is cut by a byte
// budget, so one turn can arrive as several parts (TurnPart.From); the
// renderer draws turns, so they are rejoined here. This is NOT composition
// -- the nodes were composed by whoever owns the aria -- it is the seam a
// wire budget introduced.
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

// turnsSatisfy reports whether the turns held can answer the selector. Each
// case asks for one more turn than it will show, because the oldest turn a
// walk reached may be the one the next page completes.
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

// selectShowRange is the slice of the held turns the selector names.
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
