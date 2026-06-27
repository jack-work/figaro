package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// chalkboardOf reads a node's folded chalkboard state.
func chalkboardOf(t *testing.T, s *XwalStore, id string) chalkboard.Snapshot {
	t.Helper()
	x, err := s.OpenNode(id)
	if err != nil {
		t.Fatalf("open node %s: %v", id, err)
	}
	defer x.Close()
	var last uint64
	for _, c := range x.Channels() {
		if c.Name == chanChalkboard {
			last = c.Last
		}
	}
	st, err := x.StateAt(chanChalkboard, last)
	if err != nil {
		t.Fatalf("stateAt: %v", err)
	}
	snap := chalkboard.Snapshot{}
	if err := json.Unmarshal(st, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	return snap
}

func patchSet(kv map[string]string) message.Patch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		b, _ := json.Marshal(v)
		set[k] = b
	}
	return message.Patch{Set: set}
}

func TestXwalStore_LoadoutAndConversation(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenXwalStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	patch := patchSet(map[string]string{"system.credo": "be terse"})
	l1, err := s.CreateLoadout("default", patch)
	if err != nil {
		t.Fatalf("create loadout: %v", err)
	}
	// Same name + same patch -> same node (content-version reuse).
	l1b, err := s.CreateLoadout("default", patchSet(map[string]string{"system.credo": "be terse"}))
	if err != nil || l1b != l1 {
		t.Fatalf("loadout reuse: got %q (want %q), err %v", l1b, l1, err)
	}
	// Different patch -> different node (new version).
	l2, err := s.CreateLoadout("default", patchSet(map[string]string{"system.credo": "be verbose"}))
	if err != nil || l2 == l1 {
		t.Fatalf("loadout version split failed: l2=%q l1=%q err=%v", l2, l1, err)
	}

	// A conversation inherits the loadout's chalkboard via the watermark.
	c1, err := s.CreateConversation(l1)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	snap := chalkboardOf(t, s, c1)
	if got := str(snap["system.credo"]); got != "be terse" {
		t.Fatalf("conversation credo = %q, want 'be terse' (inherited)", got)
	}
	if got := str(snap[keyLoadoutName]); got != "default" {
		t.Fatalf("loadout_name = %q, want default", got)
	}
	if str(snap[keyLoadoutVer]) == "" {
		t.Fatal("loadout_version missing")
	}

	// Two conversations off the same loadout are distinct siblings.
	c2, err := s.CreateConversation(l1)
	if err != nil || c2 == c1 {
		t.Fatalf("second conversation: c2=%q c1=%q err=%v", c2, c1, err)
	}

	// Reopen the store: index persists, nodes resolve.
	s2, err := OpenXwalStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	snap2 := chalkboardOf(t, s2, c1)
	if str(snap2["system.credo"]) != "be terse" {
		t.Fatal("after reopen, conversation lost inherited credo")
	}
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
