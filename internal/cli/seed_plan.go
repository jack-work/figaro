package cli

// seedPlan is what a subject switch decided it still owes the wire, and it is
// the ONE place that decision is written down. A switch can arrive holding
// nothing, holding the prefix two arias share, or holding a whole aria it
// showed a moment ago, and each of those owes a different read.
type seedPlan struct {
	kind seedKind
	// from is the turn the read begins at: the divergence for a shared
	// prefix, the highest held turn for a parked aria, unused otherwise.
	from int
}

type seedKind int

const (
	// seedWindow is the cold read: a window of the tail, nothing held.
	seedWindow seedKind = iota
	// seedTail reads the tail and stops at from: everything below it is held.
	seedTail
	// seedSuffix reads FORWARD from from: the reader has not moved, and what
	// is owed is the continuation of what is on screen.
	seedSuffix
	// seedNothing is a switch that owes nothing at all: the store already
	// holds every row the screen is about to paint.
	seedNothing
)
