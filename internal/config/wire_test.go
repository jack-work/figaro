package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadWith(t *testing.T, toml string) (*Loaded, error) {
	t.Helper()
	dir := t.TempDir()
	if toml != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load(dir)
}

// An absent [wire] table must yield the documented defaults, so a store
// upgraded into paging behaves without anyone editing config.toml.
func TestWireDefaults(t *testing.T) {
	l, err := loadWith(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := l.PageBudget(); got != defaultPageBudget {
		t.Errorf("PageBudget = %d, want %d", got, defaultPageBudget)
	}
	if got := l.PageBudgetMax(); got != defaultPageBudgetMax {
		t.Errorf("PageBudgetMax = %d, want %d", got, defaultPageBudgetMax)
	}
}

func TestWireExplicit(t *testing.T) {
	l, err := loadWith(t, "[wire]\npage_budget = 1024\npage_budget_max = 4096\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := l.PageBudget(); got != 1024 {
		t.Errorf("PageBudget = %d, want 1024", got)
	}
	if got := l.PageBudgetMax(); got != 4096 {
		t.Errorf("PageBudgetMax = %d, want 4096", got)
	}
}

// The clamp is the server's guard: a client may ask for nothing (take the
// default) or for too much (get the ceiling), but never for unbounded work.
func TestClampPageBudget(t *testing.T) {
	l, err := loadWith(t, "[wire]\npage_budget = 1024\npage_budget_max = 4096\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ req, want int }{
		{0, 1024},      // unset -> server default
		{-1, 1024},     // nonsense -> server default
		{2048, 2048},   // in range -> honored
		{999999, 4096}, // over ceiling -> clamped
	} {
		if got := l.ClampPageBudget(c.req); got != c.want {
			t.Errorf("ClampPageBudget(%d) = %d, want %d", c.req, got, c.want)
		}
	}
}

func TestWireValidation(t *testing.T) {
	for name, toml := range map[string]string{
		"zero budget":      "[wire]\npage_budget = 0\n",
		"negative budget":  "[wire]\npage_budget = -1\n",
		"zero max":         "[wire]\npage_budget_max = 0\n",
		"max below budget": "[wire]\npage_budget = 8192\npage_budget_max = 4096\n",
	} {
		if _, err := loadWith(t, toml); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}
