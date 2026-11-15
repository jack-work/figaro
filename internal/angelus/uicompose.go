package angelus

import (
	"slices"

	"github.com/jack-work/figaro/api/message"
	fwtree "github.com/jack-work/figaro/figwal/tree"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

// composeTurns answers a composed-cache miss from the store, keyed by node,
// so an ancestor nobody has opened composes the same as a live aria.
func (a *Angelus) composeTurns(node string, fromLT, toLT uint64) []aria.Turn {
	if a == nil || a.Backend == nil || toLT < fromLT {
		return nil
	}
	log := a.openNodeIR(node)
	if log == nil {
		return nil
	}
	entries, _ := log.ReadPage(fromLT, toLT+1, int(toLT-fromLT+1))
	if len(entries) == 0 {
		return nil
	}
	entries = append(previousTurn(log, entries[0].LT), entries...)
	entries = append(entries, tailOfLastTurn(log, entries)...)

	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	out := a.uiProj.Turns(msgs)
	if fb, ok := a.Backend.(formdelta.Backend); ok {
		seed := formdelta.Seed{}
		if prev, _ := log.ReadPage(0, entries[0].LT, 1); len(prev) > 0 {
			seed = formdelta.SeedFrom(prev[len(prev)-1])
		}
		formdelta.Attach(out, formdelta.PerRecordFrom(fb, node, seed, entries))
	}
	return slices.DeleteFunc(out, func(t aria.Turn) bool {
		return len(t.LTs) == 0 || t.LTs[0] < fromLT || t.LTs[0] > toLT
	})
}

func previousTurn(log arialog.Log[message.Message], before uint64) []arialog.Entry[message.Message] {
	var reverse []arialog.Entry[message.Message]
	for before > 0 {
		page, _ := log.ReadPage(0, before, 64)
		if len(page) == 0 {
			break
		}
		for i := len(page) - 1; i >= 0; i-- {
			reverse = append(reverse, page[i])
			if turns.Opens(page[i].Payload) {
				slices.Reverse(reverse)
				return reverse
			}
		}
		before = page[0].LT
	}
	slices.Reverse(reverse)
	return reverse
}

// openNodeIR asks for a bare node first: opening an ancestor as an aria
// would mint a handle and heal a sidecar for a frozen trunk.
func (a *Angelus) openNodeIR(node string) arialog.Log[message.Message] {
	if o, ok := a.Backend.(interface {
		OpenNode(string) arialog.Log[message.Message]
	}); ok {
		return o.OpenNode(node)
	}
	log, err := a.Backend.OpenFigIR(node)
	if err != nil {
		return nil
	}
	return log
}

func (a *Angelus) uiLineage(node string) []fwtree.Ref {
	lb, ok := a.Backend.(store.LineageBackend)
	if !ok {
		return nil
	}
	return lb.Lineage(node)
}

// tailOfLastTurn completes a turn the bracket cut off. The boundary is
// turns.Opens, not the stamped TurnID: records predating turn ids carry zero.
func tailOfLastTurn(log arialog.Log[message.Message], entries []arialog.Entry[message.Message]) []arialog.Entry[message.Message] {
	var extra []arialog.Entry[message.Message]
	for from := entries[len(entries)-1].LT + 1; ; {
		got := log.ReadFrom(from, 64)
		if len(got) == 0 {
			return extra
		}
		for _, e := range got {
			if turns.Opens(e.Payload) {
				return extra
			}
			extra = append(extra, e)
		}
		from = got[len(got)-1].LT + 1
	}
}
