package config

import "testing"

func TestPageBudgetPolicyIsNilSafe(t *testing.T) {
	var l *Loaded

	if got := l.PageBudget(); got != defaultPageBudget {
		t.Errorf("nil PageBudget() = %d, want %d", got, defaultPageBudget)
	}
	if got := l.PageBudgetMax(); got != defaultPageBudgetMax {
		t.Errorf("nil PageBudgetMax() = %d, want %d", got, defaultPageBudgetMax)
	}

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
