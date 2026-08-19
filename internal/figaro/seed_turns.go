package figaro

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// SEEDING THE COMPOSED PREFIX, the second half of the fork seed.

// donatedSeam returns the index in entries where the child's OWN records
// begin, given a donated prefix of turns, and whether the donation may be used
// at all.
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
