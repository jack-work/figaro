package angelus

import (
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
	"github.com/jack-work/figaro/internal/turns"
)

// composeTurns is the process's ONE answer to a composed-cache miss: open
// the node's log, read the bracket, compose, attach the form deltas. It is
// keyed by NODE and backed by the store, so it serves a live aria, a
// dormant one, and an ANCESTOR nobody has opened identically -- which is
// what a fork reading its inherited prefix needs and what a per-server
// source could not do.
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
	entries = append(entries, tailOfLastTurn(log, entries)...)

	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	out := a.uiProj.Turns(msgs)
	if fb, ok := a.Backend.(formdelta.Backend); ok {
		seed := formdelta.Seed{}
		if prev, _ := log.ReadPage(0, fromLT, 1); len(prev) > 0 {
			seed = formdelta.SeedFrom(prev[len(prev)-1])
		}
		formdelta.Attach(out, formdelta.PerRecordFrom(fb, node, seed, entries))
	}
	return out
}

// openNodeIR reads a node's fig IR. An ANCESTOR node is an index node, not
// an aria: opening it as one would mint a handle and heal a sidecar for a
// frozen trunk, so a backend that can read a bare node is asked first.
func (a *Angelus) openNodeIR(node string) store.Log[message.Message] {
	if o, ok := a.Backend.(interface {
		OpenNode(string) store.Log[message.Message]
	}); ok {
		return o.OpenNode(node)
	}
	log, err := a.Backend.Open(node)
	if err != nil {
		return nil
	}
	return log
}

// uiLineage is a node's ancestry for the composed cache: the same walk the
// decoded IR reads its prefix through. A backend with no notion of ancestry
// makes every aria a root.
func (a *Angelus) uiLineage(node string) []fwtree.Ref {
	lb, ok := a.Backend.(store.LineageBackend)
	if !ok {
		return nil
	}
	return lb.Lineage(node)
}

// tailOfLastTurn is the records the bracket CUT OFF: a turn whose opening
// record is inside the bracket but whose later records are not composes
// with its content missing, and the caller cannot tell that from a turn
// that had nothing to say. A bracket that BEGINS mid-turn needs no such
// repair -- composition drops records until one OPENS a turn, so the
// partial head belongs to the run below and is dropped by construction.
//
// The boundary is turns.Opens, not the stamped TurnID: a record written
// before turn ids existed carries a zero, and a rule that reads the stamp
// stops at the first such record and silently truncates the turn.
func tailOfLastTurn(log store.Log[message.Message], entries []store.Entry[message.Message]) []store.Entry[message.Message] {
	var extra []store.Entry[message.Message]
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
