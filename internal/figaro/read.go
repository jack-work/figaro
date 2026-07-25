package figaro

import "github.com/jack-work/figaro/internal/livelog/aria"

// defaultPageBudget is the fallback when no configured budget reaches the
// agent. config.Loaded.ClampPageBudget is the single policy point; this only
// covers agents constructed without one.
const defaultPageBudget = 65536

// Read pages forward from at — the catch-up half of the same paginated read
// the live MethodAriaFrame stream pushes. A (re)connecting client reads from
// its cursor, then follows the live frames; application is idempotent, so a
// catch-up/live overlap cannot double-apply.
func (a *Agent) Read(at aria.Anchor, budget int) aria.Page {
	out := a.ariaSrv.Read(at, a.pageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}

// ReadBefore pages backward from at — the other direction of the same cut, so
// a pager can walk into history without loading all of it. A backward read
// with a zero anchor is the tail, which is what `fig show -n N` asks for.
func (a *Agent) ReadBefore(at aria.Anchor, budget int) aria.Page {
	out := a.ariaSrv.ReadBefore(at, a.pageBudget(budget))
	out.Metrics = a.sessionMetrics()
	return out
}

// pageBudget resolves a requested budget. The agent has no config.Loaded, so
// it applies the same shape of policy ClampPageBudget encodes: a non-positive
// request takes the default. Wiring Loaded through to here is the remaining
// half of that single-policy-point goal.
func (a *Agent) pageBudget(requested int) int {
	if requested > 0 {
		return requested
	}
	return defaultPageBudget
}
