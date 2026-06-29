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

type rec struct{ pages []AriaRead }

func (r *rec) push(a AriaRead) { r.pages = append(r.pages, a) }

func TestServer_DeltasVersionClose(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	defer s.Subscribe(rc.push)()

	s.Open(2, "assistant")
	s.Update([]livedoc.Node{tool("running", "")})    // v0: create
	s.Update([]livedoc.Node{tool("running", "a\n")}) // v1: output appears
	s.Update([]livedoc.Node{tool("ok", "a\n")})      // v2: status flips
	s.Close()                                        // committed {lt2, v2}

	if len(rc.pages) != 4 {
		t.Fatalf("want 4 frames, got %d: %+v", len(rc.pages), rc.pages)
	}
	f0 := rc.pages[0].Live
	if f0 == nil || f0.V != 0 || f0.Role != "assistant" || len(f0.Nodes) != 1 ||
		f0.Nodes[0].ID != "n0" || f0.Nodes[0].Set["type"] != "tool" || f0.Nodes[0].Set["status"] != "running" {
		t.Fatalf("v0 create frame wrong: %+v", f0)
	}
	if f1 := rc.pages[1].Live; f1 == nil || f1.V != 1 || f1.Role != "" || f1.Nodes[0].Set["output"] != "a\n" {
		t.Fatalf("v1 frame wrong: %+v", f1)
	}
	if f2 := rc.pages[2].Live; f2 == nil || f2.V != 2 || f2.Nodes[0].Set["status"] != "ok" || f2.Nodes[0].Set["output"] != nil {
		t.Fatalf("v2 should set only status: %+v", f2)
	}
	last := rc.pages[3].Committed
	if len(last) != 1 || last[0].LT != 2 || last[0].V != 2 || last[0].Full() {
		t.Fatalf("close marker wrong: %+v", last)
	}
}

func TestServer_Unset(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	defer s.Subscribe(rc.push)()
	s.Open(2, "assistant")
	s.Update([]livedoc.Node{tool("ok", "x")}) // v0 with output
	s.Update([]livedoc.Node{tool("ok", "")})  // v1: output cleared
	f := rc.pages[1].Live
	if f == nil || len(f.Nodes) != 1 || len(f.Nodes[0].Unset) != 1 || f.Nodes[0].Unset[0] != "output" {
		t.Fatalf("expected unset[output], got %+v", f)
	}
}

func TestClient_FoldAndPromote(t *testing.T) {
	s := NewServer()
	c := NewClient()
	var done []Message
	c.OnClosed = func(m Message) { done = append(done, m) }
	defer s.Subscribe(c.Apply)()

	s.Open(2, "assistant")
	s.Update([]livedoc.Node{tool("running", "")})
	s.Update([]livedoc.Node{tool("running", "a\n")})
	s.Update([]livedoc.Node{tool("ok", "a\nb\n")})
	s.Close()

	if len(done) != 1 {
		t.Fatalf("want 1 finalized, got %d", len(done))
	}
	if m := done[0]; m.LT != 2 || len(m.Nodes) != 1 || m.Nodes[0].Output != "a\nb\n" || m.Nodes[0].Status != "ok" {
		t.Fatalf("promoted node wrong: %+v", m.Nodes)
	}
}

func TestClient_DesyncOnVersionMismatch(t *testing.T) {
	c := NewClient()
	desync := -1
	c.OnDesync = func(since int) { desync = since }

	c.Apply(AriaRead{Committed: []Committed{{LT: 1, Role: "user", Nodes: []livedoc.Node{prose("q")}}}})
	// open lt2, observe only v0
	c.Apply(AriaRead{Live: &Live{LT: 2, V: 0, Role: "assistant",
		Nodes: []NodeDelta{{ID: "n0", Set: map[string]any{"type": "prose", "markdown": "o"}}}}})
	// close says v2 — we only saw v0 → desync from last committed LT (1)
	c.Apply(AriaRead{Committed: []Committed{{LT: 2, V: 2}}})

	if desync != 1 {
		t.Fatalf("want desync from LT 1, got %d", desync)
	}
}

func TestServer_ReadSnapshot(t *testing.T) {
	s := NewServer()
	s.Commit(Message{LT: 1, Role: "user", Nodes: []livedoc.Node{prose("q")}})
	s.Open(2, "assistant")
	s.Update([]livedoc.Node{tool("running", "a\n")})

	r := s.Read(0)
	if len(r.Committed) != 1 || !r.Committed[0].Full() || r.Committed[0].LT != 1 {
		t.Fatalf("read(0) committed: %+v", r.Committed)
	}
	if r.Live == nil || r.Live.LT != 2 || len(r.Live.Nodes) != 1 || r.Live.Nodes[0].Set["output"] != "a\n" {
		t.Fatalf("read(0) live snapshot (full-set): %+v", r.Live)
	}
	if r2 := s.Read(1); len(r2.Committed) != 0 || r2.Live == nil {
		t.Fatalf("read(1) should omit committed, keep live: %+v", r2)
	}
}

func TestServer_PatchOnGrowth(t *testing.T) {
	s := NewServer()
	rc := &rec{}
	defer s.Subscribe(rc.push)()
	s.Open(2, "assistant")
	s.Update([]livedoc.Node{tool("running", "a\n")})    // v0 create (output set)
	s.Update([]livedoc.Node{tool("running", "a\nb\n")}) // v1 grow → patch
	f := rc.pages[1].Live
	if f == nil || len(f.Nodes) != 1 || f.Nodes[0].Set["output"] != nil || len(f.Nodes[0].Patch) == 0 {
		t.Fatalf("growth should be a patch not set: %+v", f.Nodes[0])
	}
	if d := f.Nodes[0].Patch["output"]; d.Ins != "b\n" || d.At != 2 || d.Del != 0 {
		t.Fatalf("patch should append 'b\\n' at 2: %+v", d)
	}
	c := NewClient()
	var got string
	c.OnLive = func(_ int, _ string, nodes []livedoc.Node) { got = nodes[0].Output }
	for _, p := range rc.pages {
		c.Apply(p)
	}
	if got != "a\nb\n" {
		t.Fatalf("client patch fold: got %q", got)
	}
}

// View must return closed messages in LT order even when they arrive
// out of order — a live-sealed message (this session) followed by a
// catch-up Read of older history. Otherwise the transcript renders the
// newest turn above older ones.
func TestClient_ViewSortedByLT(t *testing.T) {
	c := NewClient()
	// Live-seal the current turn first (higher LTs).
	c.Apply(AriaRead{Committed: []Committed{{LT: 7, Role: "user", Nodes: []livedoc.Node{prose("seven")}}}})
	c.Apply(AriaRead{Committed: []Committed{{LT: 8, Role: "assistant", Nodes: []livedoc.Node{prose("eight")}}}})
	// Then a catch-up Read(0) brings older history.
	c.Apply(AriaRead{Committed: []Committed{
		{LT: 1, Role: "user", Nodes: []livedoc.Node{prose("one")}},
		{LT: 2, Role: "assistant", Nodes: []livedoc.Node{prose("two")}},
		{LT: 7, Role: "user", Nodes: []livedoc.Node{prose("seven")}},      // already seen → skipped
		{LT: 8, Role: "assistant", Nodes: []livedoc.Node{prose("eight")}}, // already seen → skipped
	}})
	v := c.View()
	var got []int
	for _, m := range v.Closed {
		got = append(got, m.LT)
	}
	want := []int{1, 2, 7, 8}
	if len(got) != len(want) {
		t.Fatalf("View closed LTs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("View closed LTs = %v, want %v (out of order)", got, want)
		}
	}
}
