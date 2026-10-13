package store

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"
)

func burialFixture(t *testing.T) (*XwalBackend, string) {
	t.Helper()
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("l", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return be, outfit
}

// A delete tells whoever is listening, through the stream they already read.
func TestDeleteBuriesTheFormBeforeUnlinking(t *testing.T) {
	be, outfit := burialFixture(t)
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	f, err := be.form(id)
	if err != nil {
		t.Fatal(err)
	}
	sub := f.SubscribeFrom(8)
	defer sub.Close()

	if err := be.Remove(id, false); err != nil {
		t.Fatal(err)
	}

	var buried bool
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				if !buried {
					t.Fatal("the stream closed without a tombstone: the reader cannot tell a death from a silence")
				}
				return
			}
			if _, ok := ev.Applied.Entry(TombstoneKey); ok {
				buried = true
			}
		default:
			if !buried {
				t.Fatal("no tombstone reached the subscriber")
			}
			return
		}
	}
}

// THE ORDERING PROPERTY. A refused delete must leave the store exactly as the
// caller found it, and a tombstone is not undoable: burying before the
// refusal would seal a form that is still very much alive.
func TestRefusedDeleteBuriesNothing(t *testing.T) {
	be, outfit := burialFixture(t)
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := be.Fork(parent); err != nil {
		t.Fatal(err)
	}
	if err := be.Remove(parent, false); err == nil {
		t.Fatal("a delete with live branches must be refused without -r")
	}
	f, err := be.form(parent)
	if err != nil {
		t.Fatal(err)
	}
	if f.Tombstoned() {
		t.Fatal("a REFUSED delete tombstoned the form: the burial ran before the refusal")
	}
	if _, err := be.ApplyForm(parent, kv("brief", "still here")); err != nil {
		t.Fatalf("a refused delete sealed a living form: %v", err)
	}
}

// A recursive delete must drop the children's forms and read handles too.
func TestRecursiveDeleteBuriesTheWholeSet(t *testing.T) {
	be, outfit := burialFixture(t)
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(parent, kv("brief", "p")); err != nil {
		t.Fatal(err)
	}
	_, child, err := be.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(child, kv("brief", "c")); err != nil {
		t.Fatal(err)
	}
	if err := be.Remove(parent, true); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	resident := len(be.forms)
	be.mu.Unlock()
	if resident != 0 {
		t.Fatalf("%d form(s) still resident after the subtree was unlinked", resident)
	}
}

// The delete set is computed from the topology adjacency; reading a stale
// cached snapshot made a fork since the last refresh invisible, so the refusal
// did not fire and the child's caches stayed resident.
func TestDeleteSetSeesAForkMadeSinceTheLastRefresh(t *testing.T) {
	be, outfit := burialFixture(t)
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	// Warm the topology snapshot BEFORE the fork: this is what made the
	// staleness reachable.
	_ = be.Nodes()
	_, alt, err := be.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	set := be.Store().Tree().DeleteSet(parent)
	found := false
	for _, id := range set {
		if id == alt {
			found = true
		}
	}
	if !found {
		t.Fatalf("delete set %v omits the fork %s that figwal would unlink with it", set, alt)
	}
}

// mustEntry is the value a patch sets at key, for tests that assert on one.
func mustEntry(p form.Patch, key string) json.RawMessage {
	e, ok := p.Entry(key)
	if !ok {
		return nil
	}
	return e.New
}
