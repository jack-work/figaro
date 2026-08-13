package config

import (
	"testing"
	"time"
)

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

func TestSoftLimitBytes(t *testing.T) {
	if got := (*Loaded)(nil).SoftLimitBytes(); got != int64(defaultSoftLimitMB)<<20 {
		t.Fatalf("nil config: want the default ceiling, got %d", got)
	}
	var l Loaded
	if got := l.SoftLimitBytes(); got != int64(defaultSoftLimitMB)<<20 {
		t.Fatalf("unset: want the default ceiling, got %d", got)
	}
	off := 0
	l.Config.Memory.SoftLimitMB = &off
	if got := l.SoftLimitBytes(); got != 0 {
		t.Fatalf("0 must mean no ceiling, got %d", got)
	}
	small := 256
	l.Config.Memory.SoftLimitMB = &small
	if got := l.SoftLimitBytes(); got != 256<<20 {
		t.Fatalf("a configured ceiling must be honoured, got %d", got)
	}
}

func TestActorLinger(t *testing.T) {
	if got := (*Loaded)(nil).ActorLinger(); got != 2000*time.Millisecond {
		t.Fatalf("nil config: got %v", got)
	}
	var l Loaded
	ms := 250
	l.Config.Memory.ActorLingerMS = &ms
	if got := l.ActorLinger(); got != 250*time.Millisecond {
		t.Fatalf("configured: got %v", got)
	}
	neg := -5
	l.Config.Memory.ActorLingerMS = &neg
	if got := l.ActorLinger(); got != 0 {
		t.Fatalf("negative must floor at zero: got %v", got)
	}
}

func TestHandleIdle(t *testing.T) {
	if got := (*Loaded)(nil).HandleIdle(); got != 0 {
		t.Fatalf("nil config must defer to figwal's default: got %v", got)
	}
	var l Loaded
	m := 12
	l.Config.Memory.HandleIdleMinutes = &m
	if got := l.HandleIdle(); got != 12*time.Minute {
		t.Fatalf("configured: got %v", got)
	}
	never := -1
	l.Config.Memory.HandleIdleMinutes = &never
	if got := l.HandleIdle(); got != -time.Minute {
		t.Fatalf("negative passes through as never-unload: got %v", got)
	}
}

func TestFormPatchWindow(t *testing.T) {
	if got := (*Loaded)(nil).FormPatchWindow(); got != defaultFormPatchWindow {
		t.Fatalf("nil config: got %d", got)
	}
	var l Loaded
	off := 0
	l.Config.Memory.FormPatchWindow = &off
	if got := l.FormPatchWindow(); got != 0 {
		t.Fatalf("0 must mean retain everything: got %d", got)
	}
	n := 64
	l.Config.Memory.FormPatchWindow = &n
	if got := l.FormPatchWindow(); got != 64 {
		t.Fatalf("configured: got %d", got)
	}
}
