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
