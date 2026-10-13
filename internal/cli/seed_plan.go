package cli

// seedPlan is what a subject switch still owes the wire.
type seedPlan struct {
	kind seedKind
	from int // divergence for a shared prefix, highest held turn for a parked aria
}

type seedKind int

const (
	seedWindow seedKind = iota // cold: a window of the tail
	seedTail                   // the tail down to from; below it is held
	seedSuffix                 // forward from from; the screen's continuation
	seedNothing
)
