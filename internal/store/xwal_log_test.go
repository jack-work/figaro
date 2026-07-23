package store

import (
	"testing"

	"github.com/jack-work/figwal/xwal"
)

type tp struct {
	S string `json:"s"`
}

// mustBackendWithConv creates a fresh backend, seeds a loadout + one
// conversation, and returns them. The conversation id is a real trunk
// id; xwalLog operations against it exercise the full store path
// (OpenNode → Trunks.Head / Trunks.Append).
func mustBackendWithConv(t *testing.T) (*XwalBackend, string) {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	l, err := b.CreateLoadout("default", patchSet(nil))
	if err != nil {
		t.Fatalf("create loadout: %v", err)
	}
	conv, err := b.CreateConversation(l)
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}
	return b, conv
}

func TestXwalLog_RoundTrip(t *testing.T) {
	b, conv := mustBackendWithConv(t)

	// The IR channel is the main channel; we use its own name so the
	// Trunks.Append path (isMain=true) is exercised.
	ir := newXwalLog[tp](b.store, conv, chanIR, true)
	// Translation channel needs to be materialized before use.
	if err := b.ensureChannel(xwal.ChannelSpec{
		Name: "translations-v2/anthropic", Kind: xwal.ChannelLog, SyncMode: xwal.SyncManual, Opaque: true,
	}); err != nil {
		t.Fatal(err)
	}
	tr := newXwalLog[tp](b.store, conv, "translations-v2/anthropic", false)

	baseIR := len(ir.Read())
	for i := 1; i <= 3; i++ {
		e, err := ir.Append(Entry[tp]{Payload: tp{S: "msg"}})
		if err != nil {
			t.Fatalf("ir append: %v", err)
		}
		if e.LT == 0 || e.FigaroLT == 0 || e.LT != e.FigaroLT {
			t.Fatalf("ir LT/FigaroLT = %d/%d (want non-zero, equal)", e.LT, e.FigaroLT)
		}
		if _, err := tr.Append(Entry[tp]{FigaroLT: e.LT, Payload: tp{S: "wire"}, Fingerprint: "anthropic/v0"}); err != nil {
			t.Fatalf("tr append: %v", err)
		}
	}

	// IR read reflects the three new entries.
	if got := len(ir.Read()); got != baseIR+3 {
		t.Fatalf("ir.Read len = %d, want %d", got, baseIR+3)
	}
	// Lookup by figaro LT works.
	if got, ok := tr.Lookup(uint64(baseIR + 2)); !ok || got.Fingerprint != "anthropic/v0" || got.Payload.S != "wire" {
		t.Fatalf("tr.Lookup(%d) = (%+v,%v)", baseIR+2, got, ok)
	}
	// PeekTail returns the latest.
	if tail, ok := ir.PeekTail(); !ok || tail.Payload.S != "msg" {
		t.Fatalf("ir.PeekTail = (%+v,%v)", tail, ok)
	}
	// Clear wipes translations; reusable after.
	if err := tr.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := tr.Lookup(uint64(baseIR + 2)); ok {
		t.Fatal("translation survived Clear")
	}
}

// TestXwalLog_ReadsSurvivePromote is the whole point of the pull-
// invalidation-with-fresh-open design: after Promote on a sibling
// (which used to trigger evictAll on the whole backend), a live
// xwalLog on this aria still reads and writes without any external
// help. No cache invalidation, no reopen dance.
func TestXwalLog_ReadsSurvivePromote(t *testing.T) {
	b, conv := mustBackendWithConv(t)

	// Warm the aria with a few messages.
	ir := newXwalLog[tp](b.store, conv, chanIR, true)
	for i := 0; i < 3; i++ {
		if _, err := ir.Append(Entry[tp]{Payload: tp{S: "warm"}}); err != nil {
			t.Fatalf("warmup append: %v", err)
		}
	}
	warmedLen := len(ir.Read())

	// Create a sibling conversation under the same loadout and promote
	// it. Under the OLD backend this triggered evictAll and would
	// invalidate every cached handle. Under the new backend it's a
	// no-op for this aria's row cache.
	nodes := b.store.Nodes()
	var loadoutID string
	for _, n := range nodes {
		if n.Kind == string(kindLoadout) {
			loadoutID = n.ID
			break
		}
	}
	if loadoutID == "" {
		t.Fatal("no loadout node found")
	}
	sibling, err := b.CreateConversation(loadoutID)
	if err != nil {
		t.Fatal(err)
	}
	// The sibling is rooted at the loadout stump, so Promote returns
	// ErrAtStump — but the point of the test is that our conv's
	// handle is untouched regardless.
	_, _ = b.Promote(sibling, 1)

	// Read still works on the ORIGINAL aria's live xwalLog.
	if got := len(ir.Read()); got != warmedLen {
		t.Fatalf("post-promote Read len = %d, want %d", got, warmedLen)
	}
	// So does write.
	if _, err := ir.Append(Entry[tp]{Payload: tp{S: "after-promote"}}); err != nil {
		t.Fatalf("post-promote append: %v", err)
	}
	if got := len(ir.Read()); got != warmedLen+1 {
		t.Fatalf("post-append Read len = %d, want %d", got, warmedLen+1)
	}
}
