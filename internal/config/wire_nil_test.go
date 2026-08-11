package config

import "testing"

// The agent is constructed without config in tests and for ephemeral arias, so
// the accessors must answer on a nil receiver. If they did not, the agent would
// need its own fallback constant: which is exactly the duplicated-policy bug
// this project exists to kill: before this wiring, internal/figaro carried its
// own defaultPageBudget AND silently dropped the ceiling.
func TestPageBudgetPolicyIsNilSafe(t *testing.T) {
	var l *Loaded

	if got := l.PageBudget(); got != defaultPageBudget {
		t.Errorf("nil PageBudget() = %d, want %d", got, defaultPageBudget)
	}
	if got := l.PageBudgetMax(); got != defaultPageBudgetMax {
		t.Errorf("nil PageBudgetMax() = %d, want %d", got, defaultPageBudgetMax)
	}

	// The ceiling is the half that used to be missing: an agent with no config
	// must still refuse to materialize an unbounded page on a client's say-so.
	for _, c := range []struct{ req, want int }{
		{0, defaultPageBudget},          // unset -> server default
		{-1, defaultPageBudget},         // nonsense -> server default
		{1024, 1024},                    // in range -> honoured
		{1 << 30, defaultPageBudgetMax}, // absurd -> clamped
		{defaultPageBudgetMax + 1, defaultPageBudgetMax},
	} {
		if got := l.ClampPageBudget(c.req); got != c.want {
			t.Errorf("nil ClampPageBudget(%d) = %d, want %d", c.req, got, c.want)
		}
	}
}
