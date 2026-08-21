package cli

// The backward walk that makes `show` reach the tail of any aria.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
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

// gatherShowWindow walks the RAW IR backward for --verbose/--literal, the
// two views that render records rather than turns. Everything else pages
// composed turns from the daemon (show_pages.go).
func gatherShowWindow(ctx context.Context, acli *angelus.Client, figaroID string, opts showOpts) (showWindow, error) {
	var w showWindow
	before := uint64(math.MaxUint64)

	for {
		resp, err := acli.IRBefore(ctx, figaroID, 0, before, showPage)
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
		// The RAW view shows records, so its selector is a record count:
		// -n N in --verbose/--literal means N messages, not N turns.
		if !opts.all && len(w.entries) > opts.last {
			return w, nil
		}
		before = page[0].LT
	}
}

// clipToBudget drops turns from the OLDEST end until the rendered size fits
// maxBytes, and reports how many it dropped. Zero means no budget.
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
