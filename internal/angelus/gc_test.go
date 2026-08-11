package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func gcHandlers(t *testing.T) (*handlers, *store.XwalBackend) {
	t.Helper()
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}, backend
}

func gcPatch(v string) message.Patch {
	b, _ := json.Marshal(v)
	return message.Patch{Set: map[string]json.RawMessage{"v": b}}
}

func runGCHandler(t *testing.T, h *handlers, dryRun bool) rpc.GCResponse {
	t.Helper()
	params, _ := json.Marshal(rpc.GCRequest{DryRun: dryRun})
	out, err := h.gc(context.Background(), params)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	resp, ok := out.(rpc.GCResponse)
	if !ok {
		t.Fatalf("gc returned %T", out)
	}
	return resp
}

// The backlog gc exists for: outfit versions whose arias are long gone, or
// which never got one. A stump still hosting an aria must survive.
func TestGCCollectsOnlyChildlessStumps(t *testing.T) {
	h, backend := gcHandlers(t)

	live, err := backend.CreateOutfit("config", gcPatch("live"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CreateConversation(live); err != nil {
		t.Fatal(err)
	}
	// Two older versions nothing was ever born under: the shape a store
	// accumulates when an outfit is edited between mints.
	for _, v := range []string{"old-1", "old-2"} {
		if _, err := backend.CreateOutfit("config", gcPatch(v)); err != nil {
			t.Fatal(err)
		}
	}

	dry := runGCHandler(t, h, true)
	if len(dry.Stumps) != 3 {
		t.Fatalf("stumps = %d, want 3", len(dry.Stumps))
	}
	if dry.Collected != 2 {
		t.Fatalf("dry-run would collect %d, want 2", dry.Collected)
	}
	if got := len(backend.Nodes()); got != len(dry.Stumps)+2 { // + root + the live conversation
		t.Fatalf("dry run changed the store: %d nodes", got)
	}

	got := runGCHandler(t, h, false)
	if got.Collected != 2 {
		t.Fatalf("collected %d, want 2", got.Collected)
	}
	var stumps int
	for _, n := range backend.Nodes() {
		if n.Kind == "outfit" {
			stumps++
		}
	}
	if stumps != 1 {
		t.Fatalf("stumps after gc = %d, want the one still in use", stumps)
	}

	// Idempotent: a second sweep has nothing left to take.
	if again := runGCHandler(t, h, false); again.Collected != 0 {
		t.Fatalf("second sweep collected %d, want 0", again.Collected)
	}
}

// The live aria must still be readable: gc must never touch a prefix
// something reads its history through.
func TestGCLeavesLiveAriasReadable(t *testing.T) {
	h, backend := gcHandlers(t)

	live, err := backend.CreateOutfit("config", gcPatch("live"))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := backend.CreateConversation(live)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CreateOutfit("config", gcPatch("dead")); err != nil {
		t.Fatal(err)
	}

	if got := runGCHandler(t, h, false); got.Collected != 1 {
		t.Fatalf("collected %d, want 1", got.Collected)
	}
	if _, err := backend.Open(conv); err != nil {
		t.Fatalf("live aria unreadable after gc: %v", err)
	}
}
