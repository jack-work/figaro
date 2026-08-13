package config

import "testing"

// The resident-IR budget is the largest single lever on a live daemon's
// memory, and it was unbounded by default. These pin the three answers:
// nothing configured is BOUNDED, an explicit zero is unbounded, and a value
// below the floor is raised rather than honoured.
func TestIRWindowBytesDefaults(t *testing.T) {
	if got := (*Loaded)(nil).IRWindowBytes(); got != defaultIRWindowMB<<20 {
		t.Fatalf("nil config: want the default budget, got %d", got)
	}

	var l Loaded
	if got := l.IRWindowBytes(); got != defaultIRWindowMB<<20 {
		t.Fatalf("unset: want the default budget, got %d", got)
	}

	zero := 0
	l.Config.Memory.IRWindowMB = &zero
	if got := l.IRWindowBytes(); got != 0 {
		t.Fatalf("explicit 0 must mean unbounded, got %d", got)
	}

	tiny := 0
	tiny = -3
	l.Config.Memory.IRWindowMB = &tiny
	if got := l.IRWindowBytes(); got != 0 {
		t.Fatalf("negative must mean unbounded, got %d", got)
	}

	small := 1
	l.Config.Memory.IRWindowMB = &small
	if got := l.IRWindowBytes(); got != minIRWindowMB<<20 {
		t.Fatalf("a value at the floor must survive, got %d", got)
	}

	big := 32
	l.Config.Memory.IRWindowMB = &big
	if got := l.IRWindowBytes(); got != 32<<20 {
		t.Fatalf("a configured value must be honoured, got %d", got)
	}
}
