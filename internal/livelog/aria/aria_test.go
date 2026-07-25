package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

func tool(status, out string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: status, Output: out,
		Args: map[string]any{"command": "ls"}}
}
func prose(md string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: md}
}

type rec struct{ pages []Page }

func (r *rec) push(a Page) { r.pages = append(r.pages, a) }

// sealedTurn builds a complete turn for Commit.
func sealedTurn(id uint64, nodes ...livedoc.Node) Turn {
	return Turn{ID: id, Sealed: true, Nodes: nodes}
}

// turnIDs lists the ids of the parts on a page that carry content.
func turnIDs(p Page) []uint64 {
	var out []uint64
	for _, part := range p.Parts {
		if len(part.Nodes) > 0 {
			out = append(out, part.ID)
		}
	}
	return out
}

func eqIDs(t *testing.T, label string, got, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v want %v", label, got, want)
		}
	}
}

// A streaming suffix emits one frame per change, versioned, with the node
// addressed by its positional id — no separate key.
func TestServer_DeltasVersionAndFold(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	defer s.Subscribe(rc.push)()

	s.Append(1, []livedoc.Node{prose("q")}) // the prompt: node 0, already closed
	s.OpenTurn(1)                           // suffix opens at node 1
	s.Update([]livedoc.Node{tool("running", "")})
	s.Update([]livedoc.Node{tool("running", "a\n")})
	s.Update([]livedoc.Node{tool("ok", "a\n")})
	s.Close()

	if len(rc.pages) != 5 {
		t.Fatalf("want 5 frames (append + 3 updates + close), got %d", len(rc.pages))
	}

	create := rc.pages[1].LiveTail()
	if create == nil || create.V != 0 || create.From != 1 || len(create.Nodes) != 1 {
		t.Fatalf("v0 create frame wrong: %+v", create)
	}
	if create.Nodes[0].ID != 1 {
		t.Errorf("suffix starts at node 1, so the delta id must be 1, got %d", create.Nodes[0].ID)
	}
	if create.Nodes[0].Set["type"] != "tool" || create.Nodes[0].Set["status"] != "running" {
		t.Errorf("create must carry the whole node: %+v", create.Nodes[0].Set)
	}

	grow := rc.pages[2].LiveTail()
	if grow == nil || grow.V != 1 || grow.Nodes[0].Set["output"] != "a\n" {
		t.Fatalf("v1 frame wrong: %+v", grow)
	}

	flip := rc.pages[3].LiveTail()
	if flip == nil || flip.V != 2 || flip.Nodes[0].Set["status"] != "ok" {
		t.Fatalf("v2 frame wrong: %+v", flip)
	}
	if flip.Nodes[0].Set["output"] != nil {
		t.Error("an unchanged field must not be resent")
	}

	closed := rc.pages[4].LiveTail()
	if closed == nil || closed.V != 2 || len(closed.Nodes) != 0 {
		t.Fatalf("close marker should carry the last version and no nodes: %+v", closed)
	}
}

// A field that empties must unset rather than silently persist.
func TestServer_Unset(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	defer s.Subscribe(rc.push)()

	s.OpenTurn(1)
	s.Update([]livedoc.Node{tool("running", "x")})
	s.Update([]livedoc.Node{tool("running", "")})

	last := rc.pages[len(rc.pages)-1].LiveTail()
	if last == nil || len(last.Nodes) != 1 {
		t.Fatalf("want one delta, got %+v", last)
	}
	found := false
	for _, f := range last.Nodes[0].Unset {
		if f == "output" {
			found = true
		}
	}
	if !found {
		t.Fatalf("emptied output must unset: %+v", last.Nodes[0])
	}
}

// Growth of a streamed string splices rather than resending the whole value.
func TestServer_PatchOnGrowth(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	defer s.Subscribe(rc.push)()

	s.OpenTurn(1)
	s.Update([]livedoc.Node{prose("hello")})
	s.Update([]livedoc.Node{prose("hello world")})

	last := rc.pages[len(rc.pages)-1].LiveTail()
	if last == nil || len(last.Nodes) != 1 {
		t.Fatalf("want one delta: %+v", last)
	}
	if _, ok := last.Nodes[0].Patch["markdown"]; !ok {
		t.Fatalf("appending to markdown must splice, not resend: %+v", last.Nodes[0])
	}
}

// Close folds the suffix into its turn but does NOT seal it — a turn spans
// many messages, and only finishTurn ends it.
func TestServer_CloseFoldsSealDoesNot(t *testing.T) {
	s := NewServer()
	s.Append(1, []livedoc.Node{prose("q")})
	s.OpenTurn(1)
	s.Update([]livedoc.Node{tool("ok", "out")})
	s.Close()

	turns := s.Turns()
	if len(turns) != 1 || len(turns[0].Nodes) != 2 {
		t.Fatalf("close must fold the suffix in: %+v", turns)
	}
	if turns[0].Sealed {
		t.Error("close ends a message, not a turn")
	}

	// A second round reopens at the SAME boundary and resends the whole
	// streaming region, because that is what the producer recomposes. The
	// region replaces rather than appends, so the tool is not duplicated.
	s.OpenTurn(1)
	s.Update([]livedoc.Node{tool("ok", "out"), prose("answer")})
	s.Close()
	turns = s.Turns()
	if len(turns[0].Nodes) != 3 {
		t.Fatalf("want prompt + tool + answer, got %d nodes: %+v", len(turns[0].Nodes), turns[0].Nodes)
	}
	if turns[0].Nodes[1].Type != livedoc.NodeTool || turns[0].Nodes[2].Markdown != "answer" {
		t.Fatalf("second round must replace the region, not append: %+v", turns[0].Nodes)
	}

	s.Seal(nil)
	if turns = s.Turns(); !turns[0].Sealed {
		t.Error("Seal ends the turn")
	}
	if turns[0].Live != nil {
		t.Error("a sealed turn has no open suffix")
	}
}

// The client folds a page into materialized turns and reports them closed once
// sealed.
func TestClient_FoldAndPromote(t *testing.T) {
	c := NewClient()
	var closed []Message
	c.OnClosed = func(m Message) { closed = append(closed, m) }

	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Nodes: []livedoc.Node{prose("q")}}}}})
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 1, Live: &Live{From: 1, V: 0, Nodes: []NodeDelta{
			{ID: 1, Set: map[string]any{"type": "prose", "markdown": "hi"}},
		}}},
		From: 1,
	}}})
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Sealed: true}}}})

	if len(closed) != 1 {
		t.Fatalf("want one finalized turn, got %d", len(closed))
	}
	if len(closed[0].Nodes) != 2 {
		t.Fatalf("finalized turn must hold prompt + reply, got %d nodes", len(closed[0].Nodes))
	}
	if closed[0].Nodes[1].Markdown != "hi" {
		t.Errorf("streamed node did not fold: %+v", closed[0].Nodes[1])
	}
}

// Metrics reach the caller before the turn is announced closed, so a status
// line never renders stale numbers alongside fresh content.
func TestClient_AppliesMetricsBeforeClosing(t *testing.T) {
	c := NewClient()
	var order []string
	c.OnMetrics = func(Metrics) { order = append(order, "metrics") }
	c.OnClosed = func(Message) { order = append(order, "closed") }

	c.Apply(Page{
		Parts:   []TurnPart{{Turn: sealedTurn(1, prose("q"))}},
		Metrics: &Metrics{ContextTokens: 5},
	})

	if len(order) != 2 || order[0] != "metrics" || order[1] != "closed" {
		t.Fatalf("want metrics then closed, got %v", order)
	}
}

// Read pages forward from a turn cursor; ReadBefore walks the other way. The
// zero anchor with Backward is the tail.
func TestServer_ReadBothDirections(t *testing.T) {
	s := NewServer()
	for i := uint64(1); i <= 10; i++ {
		s.Commit(sealedTurn(i, prose("m")))
	}
	big := 1 << 20

	eqIDs(t, "forward all", turnIDs(s.Read(Anchor{}, big)),
		[]uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	eqIDs(t, "forward from 8", turnIDs(s.Read(Anchor{Turn: 8}, big)),
		[]uint64{8, 9, 10})

	tail := s.ReadBefore(Anchor{}, nodeSize(prose("m"))*3)
	eqIDs(t, "backward tail", turnIDs(tail), []uint64{8, 9, 10})
	if !tail.More.Before || tail.More.After {
		t.Errorf("tail page: want more before, none after; got %+v", tail.More)
	}
}

// A read that reaches the open suffix carries Live; the same conversation read
// below it does not.
func TestServer_ReadSnapshotCarriesLiveOnlyAtTheTail(t *testing.T) {
	s := NewServer()
	s.Commit(sealedTurn(1, prose("old")))
	s.Append(2, []livedoc.Node{prose("q")})
	s.OpenTurn(2)
	s.Update([]livedoc.Node{tool("running", "a\n")})

	full := s.Read(Anchor{}, 1<<20)
	live := full.LiveTail()
	if live == nil || live.From != 1 {
		t.Fatalf("a full read must expose the suffix boundary: %+v", live)
	}

	// A page budgeted to stop inside turn 1 never sees the suffix.
	short := s.Read(Anchor{}, nodeSize(prose("old")))
	if short.LiveTail() != nil {
		t.Fatalf("a page below the suffix must be immutable: %+v", short.LiveTail())
	}
}

// Retention trims the oldest materialized turns without disturbing the cursor.
func TestClient_ClosedLimitKeepsTail(t *testing.T) {
	c := NewClient()
	c.SetClosedLimit(3)
	for i := 1; i <= 6; i++ {
		c.Apply(Page{Parts: []TurnPart{{Turn: sealedTurn(uint64(i), prose("m"))}}})
	}
	v := c.View()
	if len(v.Closed) != 3 {
		t.Fatalf("want 3 retained, got %d", len(v.Closed))
	}
	if v.Closed[0].LT != 4 || v.Closed[2].LT != 6 {
		t.Fatalf("want the tail 4..6, got %d..%d", v.Closed[0].LT, v.Closed[2].LT)
	}
	if c.Cursor() != 6 {
		t.Errorf("cursor must track the newest turn, got %d", c.Cursor())
	}
}
