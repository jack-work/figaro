package figaro

import "github.com/jack-work/figaro/internal/livelog/aria"

// Read pages forward from at — the catch-up half of the same paginated read
// the live MethodAriaFrame stream pushes. A (re)connecting client reads from
// its cursor, then follows the live frames; application is idempotent, so a
// catch-up/live overlap cannot double-apply.
func (a *Agent) Read(at aria.Anchor, budget int) aria.Page {
	out := a.ariaSrv.Read(at, a.settings.ClampPageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}

// ReadBefore pages backward from at — the other direction of the same cut, so
// a pager can walk into history without loading all of it. A backward read
// with a zero anchor is the tail, which is what `fig show -n N` asks for.
func (a *Agent) ReadBefore(at aria.Anchor, budget int) aria.Page {
	out := a.ariaSrv.ReadBefore(at, a.settings.ClampPageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}
