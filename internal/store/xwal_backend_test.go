package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func TestXwalBackend_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// create = fork a loadout into a conversation
	l, err := b.CreateLoadout("default", patchSet(map[string]string{"system.credo": "be terse"}))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(l)
	if err != nil {
		t.Fatal(err)
	}

	// IR + translation logs (memoized, shared per aria)
	ir, err := b.Open(conv)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := b.OpenTranslation(conv, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	um := message.Message{Role: message.RoleUser}
	e, err := ir.Append(Entry[message.Message]{Payload: um})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{json.RawMessage(`{"x":1}`)}, Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	// re-Open returns the SAME memoized instance -> sees the append
	ir2, _ := b.Open(conv)
	if got := ir2.Read(); len(got) == 0 || got[len(got)-1].Payload.Role != message.RoleUser {
		t.Fatalf("memoized IR did not reflect append: %+v", got)
	}
	if g, ok := tr.Lookup(e.LT); !ok || g.Fingerprint != "fp" {
		t.Fatalf("translation lookup = (%+v,%v)", g, ok)
	}

	// chalkboard: inherited credo, re-derived via StateAt
	snap, err := b.ChalkboardState(conv)
	if err != nil {
		t.Fatal(err)
	}
	if str(snap["system.credo"]) != "be terse" {
		t.Fatalf("credo = %q, want 'be terse'", str(snap["system.credo"]))
	}
	// mutate via patch; re-derive sees it
	if err := b.ApplyChalkboard(conv, patchSet(map[string]string{"system.cwd": "/tmp"})); err != nil {
		t.Fatal(err)
	}
	snap, _ = b.ChalkboardState(conv)
	if str(snap["system.cwd"]) != "/tmp" {
		t.Fatalf("cwd after apply = %q", str(snap["system.cwd"]))
	}
	// Commit a turn so the set is below the fork point (the realistic
	// flow: set rides with its turn). A set with NO intervening turn sits
	// at the fork boundary and would ride only with the continuation —
	// a known edge to revisit.
	if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleAssistant}}); err != nil {
		t.Fatal(err)
	}

	// meta sidecar
	if err := b.SetMeta(conv, &AriaMeta{MessageCount: 3, TokensIn: 10}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.Meta(conv); m == nil || m.MessageCount != 3 {
		t.Fatalf("meta = %+v", m)
	}

	// list shows the conversation
	list, _ := b.List()
	found := false
	for _, a := range list {
		if a.ID == conv {
			found = true
		}
	}
	if !found {
		t.Fatalf("conversation %s not in List %+v", conv, list)
	}

	// fork: conversation branches into two; the parent is evicted/frozen
	cont, alt, err := b.Fork(conv)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	for _, id := range []string{cont, alt} {
		snap, err := b.ChalkboardState(id)
		if err != nil {
			t.Fatalf("child %s chalkboard: %v", id, err)
		}
		if str(snap["system.cwd"]) != "/tmp" {
			t.Fatalf("child %s lost inherited cwd: %q", id, str(snap["system.cwd"]))
		}
	}
}

func patchSet(kv map[string]string) message.Patch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		b, _ := json.Marshal(v)
		set[k] = b
	}
	return message.Patch{Set: set}
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func TestXwalBackend_ForkAtInterior(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l, _ := b.CreateLoadout("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)

	ir, _ := b.Open(conv)
	for _, r := range []message.Role{message.RoleUser, message.RoleAssistant, message.RoleUser} {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: r}}); err != nil {
			t.Fatal(err)
		}
	}
	// Interior fork: store.ForkAt via the backend. cont == conv (stable id).
	cont, alt, err := b.ForkAt(conv, 4) // within conv's own range (inherited prefix is frozen)
	if err != nil {
		t.Fatalf("ForkAt interior: %v", err)
	}
	if cont != conv {
		t.Fatalf("cont should equal conv (stable id): cont=%s conv=%s", cont, conv)
	}
	if alt == conv || alt == "" {
		t.Fatalf("alt must be a fresh trunk, got %q", alt)
	}
	// The alternative inherits the chalkboard prefix and is sendable.
	snap, err := b.ChalkboardState(alt)
	if err != nil {
		t.Fatalf("alt chalkboard: %v", err)
	}
	if str(snap["system.model"]) != "m" {
		t.Fatalf("alt lost inherited model: %q", str(snap["system.model"]))
	}
	altIR, err := b.Open(alt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := altIR.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}}); err != nil {
		t.Fatalf("send to forked alt: %v", err)
	}
}

func TestXwalBackend_CauterizedLoadoutFork(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l, _ := b.CreateLoadout("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)
	// Give conv a couple own turns so LT 2 (the loadout birth) is clearly an
	// inherited, ceremonial LT.
	ir, _ := b.Open(conv)
	for i := 0; i < 2; i++ {
		ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	}
	// Fork conv at LT 2 — owned by the LOADOUT. Cauterized => a NEW conversation
	// sharing the loadout, NOT a re-split of the loadout into a continuation.
	cont, sib, err := b.ForkAt(conv, 2)
	if err != nil {
		t.Fatalf("cauterized fork at loadout LT: %v", err)
	}
	if cont != conv {
		t.Fatalf("cont should stay conv: %s != %s", cont, conv)
	}
	if sib == conv || sib == l || sib == "" {
		t.Fatalf("sib must be a fresh conversation trunk, got %q", sib)
	}
	// The sibling shares the loadout chalkboard and is itself sendable.
	snap, err := b.ChalkboardState(sib)
	if err != nil {
		t.Fatalf("sib chalkboard: %v", err)
	}
	if str(snap["system.model"]) != "m" {
		t.Fatalf("sib lost the shared loadout model: %q", str(snap["system.model"]))
	}
	sibIR, _ := b.Open(sib)
	if _, err := sibIR.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}}); err != nil {
		t.Fatalf("send to cauterized sibling: %v", err)
	}
	// And the loadout still has NO live head of its own (stays ceremonial).
	if _, ok := b.Node(l); !ok {
		t.Fatalf("loadout %s should still resolve", l)
	}
}

func TestXwalBackend_LiveBlob(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l, _ := b.CreateLoadout("d", patchSet(nil))
	conv, _ := b.CreateConversation(l)

	if blob, err := b.LiveBlob(conv); err != nil || blob != nil {
		t.Fatalf("fresh trunk has no live blob: %v %q", err, blob)
	}
	// Optimistic in-place updates (last write wins).
	if err := b.SetLiveBlob(conv, []byte(`{"v":0,"nodes":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.SetLiveBlob(conv, []byte(`{"v":1,"nodes":[{"type":"prose"}]}`)); err != nil {
		t.Fatal(err)
	}
	got, err := b.LiveBlob(conv)
	if err != nil || string(got) != `{"v":1,"nodes":[{"type":"prose"}]}` {
		t.Fatalf("live blob = %q err=%v", got, err)
	}
	// Survives reopen (durable across a daemon restart).
	b.Close()
	b2, _ := NewXwalBackend(b.root)
	defer b2.Close()
	if got, _ := b2.LiveBlob(conv); string(got) != `{"v":1,"nodes":[{"type":"prose"}]}` {
		t.Fatalf("live blob not durable across reopen: %q", got)
	}
	// Clear on commit/close.
	if err := b2.ClearLive(conv); err != nil {
		t.Fatal(err)
	}
	if got, _ := b2.LiveBlob(conv); got != nil {
		t.Fatalf("live blob not cleared: %q", got)
	}
}

func TestXwalBackend_ForestVectors(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l, _ := b.CreateLoadout("d", patchSet(nil))
	c1, _ := b.CreateConversation(l) // root [0]
	c2, _ := b.CreateConversation(l) // root [1]
	// give c1 a turn so it's interior-forkable, then fork it -> a branch [0,0]
	ir, _ := b.Open(c1)
	ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	_, alt, err := b.Fork(c1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]int{}
	for _, n := range b.Nodes() {
		if n.Kind == string(kindConversation) {
			got[n.ID] = n.Vector
		}
	}
	// Sibling roots are ordered by id (random hex), so c1/c2 may land in either
	// order — assert the structure, not a fixed creation-order assignment: both
	// top-level roots have distinct length-1 vectors forming the set {[0],[1]},
	// and alt is c1's branch (its parent's vector + [0]).
	if len(got[c1]) != 1 || len(got[c2]) != 1 {
		t.Fatalf("top-level roots must have length-1 vectors: c1=%v c2=%v", got[c1], got[c2])
	}
	if !((got[c1][0] == 0 && got[c2][0] == 1) || (got[c1][0] == 1 && got[c2][0] == 0)) {
		t.Fatalf("root vectors must be {[0],[1]}: c1=%v c2=%v", got[c1], got[c2])
	}
	wantAlt := append(append([]int(nil), got[c1]...), 0)
	if len(got[alt]) != 2 || got[alt][0] != wantAlt[0] || got[alt][1] != wantAlt[1] {
		t.Fatalf("alt (branch of c1) vector = %v, want %v", got[alt], wantAlt)
	}
}
