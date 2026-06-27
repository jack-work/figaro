package stream

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/doc"
)

func patch(s *Server, id, old, new string) {
	d, _ := doc.Diff(old, new)
	s.Publish(doc.Patch(id, d))
}

func sameDocs(t *testing.T, got []doc.Block, want []doc.Block) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("block count: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Body != want[i].Body || got[i].Status != want[i].Status {
			t.Fatalf("block %d mismatch:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

func TestClient_FreshCatchupMatchesServer(t *testing.T) {
	s := NewServer()
	s.Publish(doc.Append(doc.Block{ID: "a", Kind: "text", Status: doc.StatusActive}))
	patch(s, "a", "", "hello")
	s.Publish(doc.Append(doc.Block{ID: "b", Kind: "tool", Status: doc.StatusActive}))
	patch(s, "a", "hello", "hello world")
	s.Publish(doc.SetStatus("b", doc.StatusOK))

	c := NewClient(64)
	c.Catchup(s)
	sameDocs(t, c.Doc().Blocks(), s.Blocks())
	if c.Cursor() != 5 {
		t.Errorf("cursor=%d want 5", c.Cursor())
	}
}

func TestClient_LiveFollow(t *testing.T) {
	s := NewServer()
	c := NewClient(64)
	c.Catchup(s)
	cancel := c.Follow(s)
	defer cancel()

	s.Publish(doc.Append(doc.Block{ID: "a", Kind: "text"}))
	patch(s, "a", "", "live")
	sameDocs(t, c.Doc().Blocks(), s.Blocks())
}

// The headline requirement: catch-up on disconnect. Follow, drop the
// subscription, let the server move on, then reconnect and recover the gap via
// the paginated tail — the local Doc must converge to the server's.
func TestClient_ReconnectRecoversGap(t *testing.T) {
	s := NewServer()
	s.Publish(doc.Append(doc.Block{ID: "a", Kind: "text"}))
	patch(s, "a", "", "one")

	c := NewClient(64)
	c.Catchup(s)
	cancel := c.Follow(s)

	// --- disconnect: drop the live subscription ---
	cancel()

	// server keeps working while the client is away
	patch(s, "a", "one", "one two")
	s.Publish(doc.Append(doc.Block{ID: "b", Kind: "tool", Status: doc.StatusActive}))
	patch(s, "b", "", "running...")
	s.Publish(doc.SetStatus("b", doc.StatusOK))

	// client is now stale
	if len(c.Doc().Blocks()) == len(s.Blocks()) && c.Doc().Blocks()[0].Body == s.Blocks()[0].Body {
		t.Fatal("precondition: client should be stale after disconnect")
	}

	// --- reconnect: catch up the gap, then resume live ---
	c.Catchup(s)
	cancel2 := c.Follow(s)
	defer cancel2()

	sameDocs(t, c.Doc().Blocks(), s.Blocks())

	// and live updates resume cleanly afterward
	patch(s, "b", "", "running...") // no-op-ish; ensure follow still works
	s.Publish(doc.Append(doc.Block{ID: "c", Kind: "text"}))
	patch(s, "c", "", "after reconnect")
	sameDocs(t, c.Doc().Blocks(), s.Blocks())
}

func TestClient_SnapshotPaginates(t *testing.T) {
	s := NewServer()
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("b%02d", i)
		s.Publish(doc.Append(doc.Block{ID: id, Kind: "text"}))
		patch(s, id, "", fmt.Sprintf("body %d", i))
	}
	c := NewClient(7) // tiny pages to force multi-page snapshot + tail
	c.Catchup(s)
	sameDocs(t, c.Doc().Blocks(), s.Blocks())
	if c.Doc().Len() != 50 {
		t.Fatalf("len=%d want 50", c.Doc().Len())
	}
}

func TestClient_OverlapNoDoubleApply(t *testing.T) {
	// Subscribe BEFORE catch-up so live events and the tail overlap; the cursor
	// dedup must prevent double application.
	s := NewServer()
	s.Publish(doc.Append(doc.Block{ID: "a", Kind: "text"}))
	patch(s, "a", "", "x")

	c := NewClient(64)
	cancel := c.Follow(s) // live first
	defer cancel()
	// a live event arrives during/after subscribe
	patch(s, "a", "x", "xy")
	c.Catchup(s) // catch-up tail overlaps the live event already applied
	patch(s, "a", "xy", "xyz")

	sameDocs(t, c.Doc().Blocks(), s.Blocks())
	if c.Doc().Blocks()[0].Body != "xyz" {
		t.Fatalf("body=%q want xyz", c.Doc().Blocks()[0].Body)
	}
}
