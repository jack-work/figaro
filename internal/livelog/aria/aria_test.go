package aria

import (
	"testing"
)

func tool(name, status, out string) Node {
	return Node{Type: "tool", Name: name, Status: status, Output: out,
		Args: map[string]interface{}{"command": name}}
}

type rec struct{ pages []AriaRead }

func (r *rec) push(a AriaRead) { r.pages = append(r.pages, a) }

// committedLTs flattens every committed LT delivered across a connection's pages.
func committedLTs(pages []AriaRead) []int {
	var out []int
	for _, p := range pages {
		for _, c := range p.Committed {
			out = append(out, c.LT)
		}
	}
	return out
}

func TestRead_CatchupShapes(t *testing.T) {
	s := NewServer()
	s.Commit(Message{LT: 1, Role: "user", Nodes: []Node{{ID: "u0", Version: 1, Type: "prose", Markdown: "q"}}})
	s.Open(2, "assistant")
	s.Set("n0", Node{Type: "thinking", Markdown: "thinking"})
	s.Set("n1", tool("bash", "running", "out"))

	// Fresh catch-up: committed = the closed user message (FULL), live = the open
	// assistant message's blocks.
	r := s.Read(0)
	if len(r.Committed) != 1 || r.Committed[0].LT != 1 || r.Committed[0].Closed || r.Committed[0].Role != "user" {
		t.Fatalf("catch-up committed = %+v, want one full user message", r.Committed)
	}
	if r.Live == nil || r.Live.LT != 2 || len(r.Live.Nodes) != 2 {
		t.Fatalf("catch-up live = %+v, want open msg with 2 blocks", r.Live)
	}

	// Catch-up from LT 1: committed omitted (nothing newly closed), live present.
	r = s.Read(1)
	if len(r.Committed) != 0 {
		t.Fatalf("read(1) committed should be empty, got %+v", r.Committed)
	}
	if r.Live == nil || r.Live.LT != 2 {
		t.Fatalf("read(1) live missing")
	}

	// Once closed, a fresh read returns it as a FULL committed message, no live.
	s.Close()
	r = s.Read(1)
	if len(r.Committed) != 1 || r.Committed[0].LT != 2 || r.Committed[0].Closed || len(r.Committed[0].Nodes) != 2 {
		t.Fatalf("read(1) after close = %+v, want full message LT2", r.Committed)
	}
	if r.Live != nil {
		t.Fatalf("no open message, live should be nil; got %+v", r.Live)
	}
}

func TestStream_LiveDeltasThenClosePatch(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	cancel := s.Subscribe(0, rc.push)
	defer cancel()

	s.Commit(Message{LT: 1, Role: "user", Nodes: []Node{{ID: "u0", Version: 1, Type: "prose", Markdown: "q"}}})
	s.Open(2, "assistant")
	s.Set("n1", tool("bash", "running", ""))
	s.Set("n1", tool("bash", "running", "a\n"))
	s.Set("n1", tool("bash", "ok", "a\n"))
	s.Close()

	last := rc.pages[len(rc.pages)-1]
	// Closing a message the connection streamed live yields a CLOSE-PATCH, not a
	// full re-send.
	if len(last.Committed) != 1 || last.Committed[0].LT != 2 || !last.Committed[0].Closed || len(last.Committed[0].Nodes) != 0 {
		t.Fatalf("close should be a patch {LT2,Closed}; got %+v", last.Committed)
	}
	// Block n1's versions advanced across the live pushes.
	maxV := 0
	for _, p := range rc.pages {
		if p.Live != nil {
			for _, n := range p.Live.Nodes {
				if n.ID == "n1" && n.Version > maxV {
					maxV = n.Version
				}
			}
		}
	}
	if maxV < 3 {
		t.Fatalf("n1 should reach version >=3 over the stream, got %d", maxV)
	}
}

func TestInvariant_LTAppearsOncePerConnection(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	cancel := s.Subscribe(0, rc.push)
	defer cancel()
	for lt := 1; lt <= 4; lt++ {
		s.Open(lt, "assistant")
		s.Set("n", Node{Type: "prose", Markdown: "m"})
		s.Close()
	}
	seen := map[int]int{}
	for _, lt := range committedLTs(rc.pages) {
		seen[lt]++
	}
	for lt, n := range seen {
		if n > 1 {
			t.Fatalf("invariant violated: LT %d appeared %d times in committed", lt, n)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct committed LTs, got %d", len(seen))
	}
}

// The headline: a client disconnects mid-message, the server moves on, and the
// client reconnects from its cursor and converges exactly — no re-delivery of
// already-final messages, the gap recovered as full content.
func TestReconnect_ConvergesFromCursor(t *testing.T) {
	s := NewServer()
	c := NewClient()
	s.Commit(Message{LT: 1, Role: "user", Nodes: []Node{{ID: "u0", Version: 1, Type: "prose", Markdown: "q"}}})

	cancel := s.Subscribe(c.Cursor(), c.Apply) // streams: committed LT1 + opens LT2
	s.Open(2, "assistant")
	s.Set("n0", Node{Type: "prose", Markdown: "partial"})
	cursor := c.Cursor()
	if cursor != 1 {
		t.Fatalf("cursor after streaming open msg = %d, want 1 (LT2 not yet closed)", cursor)
	}

	cancel() // --- disconnect ---

	// Server works while the client is away.
	s.Set("n0", Node{Type: "prose", Markdown: "partial then done"})
	s.Close() // LT2 closes
	s.Commit(Message{LT: 3, Role: "user", Nodes: []Node{{ID: "u1", Version: 1, Type: "prose", Markdown: "again"}}})

	// --- reconnect from cursor: one read recovers the gap ---
	c.Apply(s.Read(cursor))

	got := c.View()
	if len(got.Closed) != 3 {
		t.Fatalf("closed=%d want 3 (LT1,2,3)", len(got.Closed))
	}
	if got.Closed[1].LT != 2 || len(got.Closed[1].Nodes) != 1 || got.Closed[1].Nodes[0].Markdown != "partial then done" {
		t.Fatalf("LT2 not recovered with final content: %+v", got.Closed[1])
	}
	if got.Open != nil {
		t.Fatalf("no open message after reconnect; got %+v", got.Open)
	}
}
