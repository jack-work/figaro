package cli

// The backward walk that makes `show` reach the tail of any aria.
//
// THE BUG THIS EXISTS TO KILL. `show` used to pull its history with one
// forward read from LT 0, and the angelus caps a single aria.read at
// ariaReadHardCap (1000) entries. Every selector then paged that prefix in
// memory. On a 4000 message aria the result was not a clipped view, it was a
// CONFIDENT WRONG ONE: `show` printed "499 turns", `show -a` printed turns
// 1..499 as if that were everything, and `--from 1900` answered "no turn in
// range". The tail was unreachable at any N, and nothing anywhere said so,
// because the response's Total was read and discarded.
//
// The fix is to walk BACKWARD from the tail instead, a page at a time, and to
// stop as soon as the requested window is covered. `show` is a tail-first
// verb: the default view is the last N turns, and the tail is the one region
// that must always be reachable, no matter how long the aria is.
//
// The store already supports this: aria.read's Before cursor is a keyset read
// that returns the LAST n entries below a cursor (store.readPage), so paging
// backward costs one call per page and never scans the head.
//
// This does not replace the turn-aware server-side paging the SEAM comment in
// aria.go describes. It is the client-side walk that makes `show` correct
// today, with the same call it always made.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

// showPage is how many IR entries one backward step asks for. It matches the
// angelus's own per-call ceiling: asking for more gets silently clamped, and
// asking for less would only mean more round trips for the same bytes.
const showPage = 1000

// showWindow is the result of the walk: the entries held, whether the walk
// reached the head of the aria, and the total the store reports.
type showWindow struct {
	entries []store.Entry[message.Message]
	// deltas is each record's form-state window, keyed by LT, as the hub
	// assembled it (the client holds neither the stamps nor the store).
	deltas map[uint64]map[string]livedoc.FormDelta
	total  int  // entries in the whole aria, from the store
	atHead bool // the oldest entry held is the aria's first
	pages  int  // backward reads issued, for the diagnostics

	// fromHead marks the legacy fallback: the window was read FORWARD from
	// LT 0 and may stop short of the tail. Without this a truncated forward
	// read reports itself as a tail, which is the original bug wearing the
	// new code's words.
	fromHead bool
}

// gatherShowWindow walks backward until opts is satisfiable.
//
// It reads whole pages, so it usually holds somewhat more than the selection
// needs; that slack is what lets the composer see a turn's head before the
// turn is rendered. A turn cut in half by a page boundary is dropped rather
// than shown as if it began there (see trimPartialHead), because a turn whose
// prompt is missing renders as an answer to nothing.
func gatherShowWindow(ctx context.Context, acli *angelus.Client, figaroID string, opts showOpts) (showWindow, error) {
	var w showWindow
	before := uint64(math.MaxUint64)

	for {
		resp, err := acli.AriaReadBefore(ctx, figaroID, 0, before, showPage)
		if err != nil {
			return w, err
		}
		w.total = resp.Total
		if len(resp.Entries) == 0 {
			w.atHead = true
			return w, nil
		}
		page := make([]store.Entry[message.Message], len(resp.Entries))
		for i, e := range resp.Entries {
			page[i].LT = e.LT
			if err := json.Unmarshal(e.Payload, &page[i].Payload); err != nil {
				return w, fmt.Errorf("parse LT=%d: %w", e.LT, err)
			}
			if len(e.FormDeltas) > 0 {
				if w.deltas == nil {
					w.deltas = map[uint64]map[string]livedoc.FormDelta{}
				}
				w.deltas[e.LT] = e.FormDeltas
			}
		}
		w.entries = append(page, w.entries...)
		w.pages++
		if len(w.entries) >= w.total {
			w.atHead = true
			return w, nil
		}
		if windowSatisfies(w, opts) {
			return w, nil
		}
		before = page[0].LT
	}
}

// windowSatisfies reports whether the entries held can already answer the
// selector. Each case asks for one more turn than it will show, because the
// oldest turn in a partial window may be missing its head.
func windowSatisfies(w showWindow, opts showOpts) bool {
	if opts.all {
		return w.atHead
	}
	ts := composeTurns(w.entries)
	if len(ts) == 0 {
		return false
	}
	switch {
	case opts.from >= 0:
		// Reaching turn `from` means holding a turn strictly older than it,
		// which is the proof that `from` itself is whole.
		return ts[0].ID < uint64(opts.from)
	case opts.before >= 0:
		n := 0
		for _, t := range ts {
			if t.ID < uint64(opts.before) {
				n++
			}
		}
		return opts.last > 0 && n > opts.last
	default:
		return opts.last > 0 && len(ts) > opts.last
	}
}

// composeTurns is the turn derivation `show` renders from. Turn ids are
// PERSISTED (figaro.appendMsg stamps every durable write), so a window read
// from the tail carries the same ids the head would have given it.
func composeTurns(entries []store.Entry[message.Message]) []aria.Turn {
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	turns.StampIDs(msgs)
	return compose.Turns(msgs)
}

// derivedIDs reports whether any message in the window arrived WITHOUT a
// stored turn id, which means StampIDs derived it by counting from the first
// entry held.
//
// That is only honest when the window starts at the head. A legacy aria
// (written before turn ids were stored) read from the tail would be numbered
// from 1 at an arbitrary point in its history, and `show` prints those
// numbers as the coordinate `send`/`fork <id>:<turn>` takes. Printing a
// confident wrong address is the failure this whole file is about, so the
// caller falls back to the forward read when this is true.
func derivedIDs(entries []store.Entry[message.Message]) bool {
	for i := range entries {
		if entries[i].Payload.TurnID == 0 && turns.Opens(entries[i].Payload) {
			return true
		}
	}
	return false
}

// trimPartialHead drops the oldest turn of a window that does not start at
// the head, unless dropping it would leave nothing. The turn is not wrong,
// it is INCOMPLETE: the page boundary fell inside it, so its prompt and its
// first nodes are older than anything held.
func trimPartialHead(ts []aria.Turn, atHead bool) []aria.Turn {
	if atHead || len(ts) <= 1 {
		return ts
	}
	return ts[1:]
}

// clipToBudget drops turns from the OLDEST end until the rendered size fits
// maxBytes, and reports how many it dropped. Zero means no budget.
//
// Which end is clipped is the whole point: `show` is how a tail gets
// snapshotted, so the newest turn is the one that must survive a budget.
func clipToBudget(ts []aria.Turn, maxBytes int) ([]aria.Turn, int) {
	if maxBytes <= 0 || len(ts) == 0 {
		return ts, 0
	}
	size := func(t aria.Turn) int {
		n := len(t.Inquiry)
		for _, node := range t.Nodes {
			n += len(node.Markdown) + len(node.Output) + len(node.Summary)
		}
		return n
	}
	total := 0
	for _, t := range ts {
		total += size(t)
	}
	drop := 0
	for drop < len(ts)-1 && total > maxBytes {
		total -= size(ts[drop])
		drop++
	}
	return ts[drop:], drop
}

// showNote prints a line to STDERR about what the window is and is not.
// Stderr on purpose: `figaro show > snapshot.txt` must not have diagnostics
// land in the file it is snapshotting.
func showNote(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "show: "+format+"\n", args...)
}
