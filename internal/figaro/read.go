package figaro

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// hydrate seeds the aria server from the log: an agent restored for a dormant
// aria holds nothing until turns seal.
func (a *Agent) hydrate() {
	a.hydrateMu.Lock()
	defer a.hydrateMu.Unlock()
	if a.ariaSrv.LastTurn() > 0 || a.ariaSrv.HasOpen() {
		return
	}
	if a.seedTurns != nil && a.figLog != nil {
		if from, ok := a.seedTurns(a.id, a.ariaSrv); ok {
			a.ariaSrv.AppendSealed(a.composeFrom(from))
			return
		}
	}
	a.ariaSrv.AdoptIfEmpty(a.materializeTurns())
}

func (a *Agent) turnCursor() uint64 {
	if last := a.ariaSrv.LastTurn(); last > 0 {
		return last
	}
	return a.tailTurnID()
}

// Read pages forward from at. Application is idempotent, so a catch-up/live overlap cannot double-apply.
func (a *Agent) Read(at aria.Anchor, budget int) aria.Page {
	a.hydrate()
	out := a.ariaSrv.Read(at, a.settings.ClampPageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}

func (a *Agent) ReadUntil(at, until aria.Anchor, budget int) aria.Page {
	a.hydrate()
	out := a.ariaSrv.ReadUntil(at, until, a.settings.ClampPageBudget(budget))
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

// TurnServer is the agent's materialized turns, for a reader seeding a fork.
func (a *Agent) TurnServer() *aria.Server { return a.ariaSrv }
