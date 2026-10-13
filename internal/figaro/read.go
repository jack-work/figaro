package figaro

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// hydrate seeds the aria server from the log: an agent restored for a dormant
// aria holds nothing until turns seal.
func (a *Agent) hydrate() {
	if a.ariaSrv.LastTurn() > 0 || a.ariaSrv.HasOpen() {
		return
	}
	a.ariaSrv.AdoptIfEmpty(a.materializeTurns())
}

// Read pages forward from at. Application is idempotent, so a catch-up/live overlap cannot double-apply.
func (a *Agent) Read(at aria.Anchor, budget int) aria.Page {
	a.hydrate()
	out := a.ariaSrv.Read(at, a.settings.ClampPageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}

// ReadBefore pages backward from at; a zero anchor is the tail. floor stops
// the walk from below, inclusive; zero is no floor.
func (a *Agent) ReadBefore(at, floor aria.Anchor, budget int) aria.Page {
	a.hydrate()
	out := a.ariaSrv.ReadBefore(at, floor, a.settings.ClampPageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}
