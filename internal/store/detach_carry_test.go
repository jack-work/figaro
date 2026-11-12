package store_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

func carryNothing([]string) error { return nil }

// El Inspector XV: a store nothing has wired to carry narrowing must refuse
// a delete that detaches a fork, not detach it bare.
func TestABareStoreRefusesADeleteThatWouldDetachAFork(t *testing.T) {
	b, outfit := ttlPresentBackend(t)
	parent, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	_, child, err := b.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Promote(child, 1); err != nil {
		t.Fatal(err)
	}
	err = b.Remove(parent, true)
	if !errors.Is(err, store.ErrNoDetachCarry) {
		t.Fatalf("delete of %s with its fork drawn above it and no carry wired = %v, want ErrNoDetachCarry", parent, err)
	}
	if n, _ := b.Node(child); n.Parent != parent {
		t.Fatalf("a refused delete detached the fork: it is stored under %q", n.Parent)
	}

	var carried []string
	b.SetDetachCarry(func(ids []string) error { carried = ids; return nil })
	if err := b.Remove(parent, true); err != nil {
		t.Fatalf("with a carry wired: %v", err)
	}
	if !slices.Contains(carried, child) {
		t.Fatalf("the carry was handed %v, want the fork %s", carried, child)
	}
}
