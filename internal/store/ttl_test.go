package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// newTTLBackend is a store with one outfit, the shape every other test here
// starts from.
func newTTLBackend(t *testing.T) *XwalBackend {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func mustNewAria(t *testing.T, b *XwalBackend) string {
	t.Helper()
	outfit, err := b.CreateOutfit("ttl", message.Patch{Set: map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setBoardKey(t *testing.T, b *XwalBackend, id, key, val string) {
	t.Helper()
	raw, _ := json.Marshal(val)
	if _, err := b.ApplyForm(id, message.Patch{Set: map[string]json.RawMessage{key: raw}}); err != nil {
		t.Fatal(err)
	}
}

func removeBoardKey(t *testing.T, b *XwalBackend, id, key string) {
	t.Helper()
	if _, err := b.ApplyForm(id, message.Patch{Remove: []string{key}}); err != nil {
		t.Fatal(err)
	}
}

func TestParseTTLUnits(t *testing.T) {
	cases := map[string]time.Duration{
		"":     0,
		"90m":  90 * time.Minute,
		"36h":  36 * time.Hour,
		"30d":  30 * 24 * time.Hour,
		"2w":   14 * 24 * time.Hour,
		"1.5d": 36 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseTTL(in)
		if err != nil {
			t.Fatalf("ParseTTL(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseTTL(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseTTL("tomorrow"); err == nil {
		t.Error("ParseTTL(\"tomorrow\") should refuse; a bad lifetime must not become a deadline")
	}
}

// A malformed ttl clears the lifetime rather than inventing one: the failure
// this prevents is a typo deleting an aria.
func TestTTLOfNonStringClears(t *testing.T) {
	p := message.Patch{Set: map[string]json.RawMessage{SystemTTLKey: json.RawMessage(`{"h":3}`)}}
	raw, spoke := ttlOf(p)
	if !spoke || raw != "" {
		t.Fatalf("ttlOf(non-string) = (%q,%v), want (\"\",true)", raw, spoke)
	}
}

func TestTTLOfRemoveClears(t *testing.T) {
	raw, spoke := ttlOf(message.Patch{Remove: []string{SystemTTLKey}})
	if !spoke || raw != "" {
		t.Fatalf("ttlOf(remove) = (%q,%v), want (\"\",true)", raw, spoke)
	}
}

func TestTTLOfSilentPatch(t *testing.T) {
	p := message.Patch{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"x"`)}}
	if _, spoke := ttlOf(p); spoke {
		t.Error("a patch that never names system.ttl must not touch the deadline")
	}
}

// The sidecar is the reaper's whole index: setting the key on a board must
// leave a deadline readable WITHOUT opening the node again.
func TestTTLCommittedStampsSidecar(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)

	before := time.Now().UnixMilli()
	setBoardKey(t, b, id, SystemTTLKey, "48h")

	meta, err := b.Meta(id)
	if err != nil || meta == nil {
		t.Fatalf("Meta(%s) = %v, %v", id, meta, err)
	}
	if meta.TTLMS != (48 * time.Hour).Milliseconds() {
		t.Errorf("sidecar ttl_ms = %d, want %d", meta.TTLMS, (48 * time.Hour).Milliseconds())
	}
	if meta.CreatedAtMS < before {
		t.Errorf("created_at_ms = %d, want >= %d (first seen is now for a node with no stamp)",
			meta.CreatedAtMS, before)
	}

	entries := ScanTTL(b.root)
	e, ok := entries[id]
	if !ok {
		t.Fatalf("ScanTTL did not find %s; the sweep would never see it", id)
	}
	if e.DeadlineMS != meta.CreatedAtMS+meta.TTLMS {
		t.Errorf("deadline %d, want created+ttl %d", e.DeadlineMS, meta.CreatedAtMS+meta.TTLMS)
	}
}

// Clearing the key must clear the deadline, or a lifetime could never be
// revoked once stated.
func TestTTLClearedRemovesDeadline(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoardKey(t, b, id, SystemTTLKey, "1h")
	if len(b.TTLEntries()) != 1 {
		t.Fatalf("after set: %d entries, want 1", len(b.TTLEntries()))
	}
	removeBoardKey(t, b, id, SystemTTLKey)
	if n := len(b.TTLEntries()); n != 0 {
		t.Fatalf("after remove: %d entries, want 0", n)
	}
	if _, ok := ScanTTL(b.root)[id]; ok {
		t.Error("ScanTTL still reports a lifetime after the key was removed")
	}
}

// A ttl shorter than the node's age is due at once: "expires from when it was
// created", not from when the key was written.
func TestTTLDueCountsFromCreation(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoardKey(t, b, id, SystemTTLKey, "1h")

	meta, _ := b.Meta(id)
	meta.CreatedAtMS = time.Now().Add(-3 * time.Hour).UnixMilli()
	if err := b.SetMeta(id, meta); err != nil {
		t.Fatal(err)
	}
	b.ttlMu.Lock()
	b.ttl = nil // force a re-scan, as a fresh daemon would
	b.ttlMu.Unlock()

	due := b.TTLDue(time.Now().UnixMilli())
	if len(due) != 1 || due[0].ID != id {
		t.Fatalf("TTLDue = %v, want exactly %s", due, id)
	}
	if got := b.TTLDue(time.Now().Add(-4 * time.Hour).UnixMilli()); len(got) != 0 {
		t.Errorf("TTLDue before the deadline = %v, want none", got)
	}
}

// A node with no lifetime never enters the index: the reaper's working set is
// what somebody opted in, not the store.
func TestTTLIndexHoldsOnlyOptIns(t *testing.T) {
	b := newTTLBackend(t)
	quiet := mustNewAria(t, b)
	setBoardKey(t, b, quiet, "mantra", "no lifetime here")
	if n := len(b.TTLEntries()); n != 0 {
		t.Fatalf("%d entries for a store where nobody set a ttl, want 0", n)
	}
}
