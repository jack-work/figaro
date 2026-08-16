package figaro

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// SEEDING THE COMPOSED PREFIX, the second half of the fork seed.
//
// Phase 4 measured it by identity and stopped: two branches of one trunk
// compose the same history into separate node structs, and the minted strings
// are the tool nodes' Input and Output, which dominate by bytes. The decoded
// layer's fix (store: seed at open) shares the ENTRIES; this shares the NODES
// composed from them.
//
// The donor is an ancestor that already materialized its turns. Its turns
// below the child's fork point are, node for node, what the child would
// compose -- so the child appends them by reference and composes only the
// records it owns.
//
// A DONATION IS REFUSED UNLESS IT IS PROVABLY A PREFIX. Composition is
// per-turn local: a suffix that begins at a turn boundary composes to the same
// turns as it does inside the whole walk. That is the ONLY property this
// splice rests on, so the guards check exactly it, and every doubt costs one
// full composition rather than serving nodes from the wrong history.

// donatedSeam returns the index in entries where the child's OWN records
// begin, given a donated prefix of turns, and whether the donation may be used
// at all.
//
// The guards, in the order they can fail:
//
//	no donation, or turn ids not strictly ascending -> refuse
//	entries not partitioned at the seam (a record of a donated turn appearing
//	  after a record of a later one) -> refuse
//	the donated turn count not equal to the distinct turn ids at or below the
//	  seam -> refuse: the ancestor holds a different history there
//
// The last is the one that matters and it costs one pass over records already
// decoded -- no composition, no decode, no map.
func donatedSeam(donated []aria.Turn, entries []store.Entry[message.Message]) (int, bool) {
	if len(donated) == 0 || len(entries) == 0 {
		return 0, false
	}
	for i := 1; i < len(donated); i++ {
		if donated[i].ID <= donated[i-1].ID {
			return 0, false
		}
	}
	seam := donated[len(donated)-1].ID

	at := len(entries)
	distinct := 0
	var lastTurn uint64
	for i, e := range entries {
		t := e.Payload.TurnID
		if t > seam {
			at = i
			break
		}
		if t != lastTurn {
			// Turn ids must be non-decreasing within the prefix, or the
			// entries are not grouped the way composition assumes.
			if t < lastTurn {
				return 0, false
			}
			distinct++
			lastTurn = t
		}
	}
	// Nothing above the seam is legal (the child has appended nothing yet);
	// nothing BELOW it is not: that would mean the donation describes turns
	// this log does not contain.
	if distinct != len(donated) {
		return 0, false
	}
	// Every remaining record must belong to a turn above the seam.
	for _, e := range entries[at:] {
		if e.Payload.TurnID <= seam {
			return 0, false
		}
	}
	return at, true
}

// spliceDonated joins a donated prefix to a composition of the records above
// the seam. compose is called at most once, on the suffix, and only when the
// donation is provably a prefix; otherwise the caller composes everything.
//
// The donated turns are appended BY REFERENCE. They are the ancestor's, and
// the held-view law applies to them exactly as it does downstairs: what a
// reader holds is never edited, only succeeded.
func spliceDonated(
	donated []aria.Turn,
	entries []store.Entry[message.Message],
	compose func(from int, rest []store.Entry[message.Message]) []aria.Turn,
) ([]aria.Turn, bool) {
	at, ok := donatedSeam(donated, entries)
	if !ok {
		return nil, false
	}
	own := compose(at, entries[at:])
	out := make([]aria.Turn, 0, len(donated)+len(own))
	out = append(out, donated...)
	return append(out, own...), true
}

// composeSealedTurns is the ONE walk that builds an aria's sealed turns:
// construction, hydration and every re-materialization go through it. When an
// ancestor offers its composed prefix and the offer is provably a prefix of
// THIS log, the child composes only the records it owns.
//
// The refusal path is the old behaviour, unchanged and unconditional: compose
// everything. That is what a process without a live ancestor does, which is
// most of them.
func (a *Agent) composeSealedTurns(entries []store.Entry[message.Message]) []aria.Turn {
	if a.turnDonor != nil {
		if donated := a.turnDonor(a.id); len(donated) > 0 {
			if out, ok := spliceDonated(donated, entries, func(at int, rest []store.Entry[message.Message]) []aria.Turn {
				turns := a.projTurns(unwrapMessages(rest))
				a.attachFormDeltasFrom(turns, entries, at)
				return turns
			}); ok {
				return out
			}
		}
	}
	return a.attachFormDeltas(a.projTurns(unwrapMessages(entries)), entries)
}

// TurnsBelow is the donor half: the sealed turns this agent holds whose nodes
// all sit below figaroLT, by reference. A turn straddling the boundary is not
// offered -- the splice needs whole turns, and half a turn is the one shape
// composition is not local over.
func (a *Agent) TurnsBelow(figaroLT uint64) []aria.Turn {
	if a.ariaSrv == nil || figaroLT == 0 {
		return nil
	}
	turns := a.ariaSrv.Turns()
	keep := 0
	for _, t := range turns {
		if !turnBelow(t, figaroLT) {
			break
		}
		keep++
	}
	return turns[:keep]
}

func turnBelow(t aria.Turn, figaroLT uint64) bool {
	for _, n := range t.Nodes {
		for _, lt := range n.LTs {
			if lt >= figaroLT {
				return false
			}
		}
	}
	return len(t.Nodes) > 0
}
