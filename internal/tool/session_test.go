package tool

import (
	"testing"
	"time"
)

func TestSessionRegistry_ScopeIsolation(t *testing.T) {
	reg := NewSessionRegistry(DefaultSessionTTL)
	a := reg.Create("scope-a", "true")
	reg.Create("scope-b", "true")

	if _, ok := reg.Get("scope-a", a.ID); !ok {
		t.Fatal("session should be visible from its own scope")
	}
	if _, ok := reg.Get("scope-b", a.ID); ok {
		t.Fatal("session must not be visible from another scope")
	}
	if got := reg.List("scope-a"); len(got) != 1 {
		t.Fatalf("List(scope-a) = %d sessions, want 1", len(got))
	}
}

func TestSessionRegistry_ReapsFinished(t *testing.T) {
	reg := NewSessionRegistry(40 * time.Millisecond)
	s := reg.Create("x", "true")
	s.finish(0) // mark exited now

	if _, ok := reg.Get("x", s.ID); !ok {
		t.Fatal("finished session should linger until TTL elapses")
	}
	time.Sleep(70 * time.Millisecond)
	if _, ok := reg.Get("x", s.ID); ok {
		t.Fatal("finished session should be reaped after TTL")
	}
}

func TestSessionRegistry_RunningNotReaped(t *testing.T) {
	reg := NewSessionRegistry(10 * time.Millisecond)
	s := reg.Create("x", "sleep")
	time.Sleep(30 * time.Millisecond)
	if _, ok := reg.Get("x", s.ID); !ok {
		t.Fatal("running session must never be reaped")
	}
}

func TestCapBuffer_DropsFront(t *testing.T) {
	b := capBuffer{cap: 4}
	b.Write([]byte("abcdef"))
	if got := b.String(); got != "cdef" {
		t.Fatalf("capBuffer = %q, want %q", got, "cdef")
	}
}
