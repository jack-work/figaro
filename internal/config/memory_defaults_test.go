package config

import (
	"testing"
	"time"
)

// The resident-IR budget is the largest single lever on a live daemon's
// memory, and CONFIG NO LONGER OWNS IT: the store bounds itself
// (store.DefaultIRBudgetBytes) and this reports only what a config file said.
// The three answers pinned here are the ones a caller must be able to tell
// apart -- nothing configured, an explicit unbounded, and a value below the
// floor -- because conflating the first two is what let a bare backend run
// unbounded.
func TestIRWindowBytesConfigured(t *testing.T) {
	if got, set := (*Loaded)(nil).IRWindowBytes(); set || got != 0 {
		t.Fatalf("nil config must configure nothing, got %d set=%v", got, set)
	}

	var l Loaded
	if got, set := l.IRWindowBytes(); set || got != 0 {
		t.Fatalf("unset must configure nothing, got %d set=%v", got, set)
	}

	zero := 0
	l.Config.Memory.IRWindowMB = &zero
	if got, set := l.IRWindowBytes(); !set || got != 0 {
		t.Fatalf("explicit 0 is a configured unbounded, got %d set=%v", got, set)
	}

	neg := -3
	l.Config.Memory.IRWindowMB = &neg
	if got, set := l.IRWindowBytes(); !set || got != 0 {
		t.Fatalf("negative is a configured unbounded, got %d set=%v", got, set)
	}

	small := 1
	l.Config.Memory.IRWindowMB = &small
	if got, set := l.IRWindowBytes(); !set || got != minIRWindowMB<<20 {
		t.Fatalf("a value at the floor must survive, got %d set=%v", got, set)
	}

	big := 32
	l.Config.Memory.IRWindowMB = &big
	if got, set := l.IRWindowBytes(); !set || got != 32<<20 {
		t.Fatalf("a configured value must be honoured, got %d set=%v", got, set)
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

// The translation cache carries the same unset/explicit-zero contract as the
// IR's, because a reader who learns one should not have to learn the other.
func TestTranslationWindowBytesConfigured(t *testing.T) {
	if got, set := (*Loaded)(nil).TranslationWindowBytes(); set || got != 0 {
		t.Fatalf("nil config must configure nothing, got %d set=%v", got, set)
	}
	var l Loaded
	if got, set := l.TranslationWindowBytes(); set || got != 0 {
		t.Fatalf("unset must configure nothing, got %d set=%v", got, set)
	}
	zero := 0
	l.Config.Memory.TranslationWindowMB = &zero
	if got, set := l.TranslationWindowBytes(); !set || got != 0 {
		t.Fatalf("explicit 0 is a configured unbounded, got %d set=%v", got, set)
	}
	small := 1
	l.Config.Memory.TranslationWindowMB = &small
	if got, set := l.TranslationWindowBytes(); !set || got != minIRWindowMB<<20 {
		t.Fatalf("below the floor must be raised, got %d set=%v", got, set)
	}
}
