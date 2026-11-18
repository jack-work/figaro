package figaro

// HoldSpoolWrites makes every spool write wait for gate, the way a stalled fsync would.
func HoldSpoolWrites(s *Spool, gate <-chan struct{}) {
	s.hold = func() { <-gate }
}
