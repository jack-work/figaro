package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"math"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/sdk/aria"
)

type irEntry struct {
	LT      uint64
	Payload message.Message
}

// showPage matches the angelus per-call ceiling; more is silently clamped.
const showPage = 1000

type showWindow struct {
	entries  []irEntry
	deltas   map[uint64]map[string]livedoc.FormDelta // keyed by LT
	total    int
	atHead   bool
	pages    int
	fromHead bool // read forward from LT 0; may stop short of the tail
}

// gatherShowWindow walks the raw IR backward for the two record views, --verbose
// and --literal.
func gatherShowWindow(ctx context.Context, acli *sdk.Angelus, figaroID string, opts showOpts) (showWindow, error) {
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
		page := make([]irEntry, len(resp.Entries))
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
		// -n N here means N records, not N turns.
		if !opts.all && len(w.entries) > opts.last {
			return w, nil
		}
		before = page[0].LT
	}
}

// clipToBudget drops from the oldest end; maxBytes 0 means no budget.
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

// showNote goes to stderr so a redirected `show` stays clean.
func showNote(format string, args ...any) {
	fmt.Fprintf(stderrw, "show: "+format+"\n", args...)
}
