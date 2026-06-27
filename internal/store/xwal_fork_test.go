package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figwal/xwal"
)

// appendIR appends a user message to a node's IR (raw, for test setup).
func appendIR(t *testing.T, s *XwalStore, id, text string) {
	t.Helper()
	x, err := s.OpenNode(id)
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	defer x.Close()
	body, _ := json.Marshal(map[string]any{"role": "user", "text": text})
	if _, err := x.AppendMain(body, nil); err != nil {
		t.Fatalf("append IR: %v", err)
	}
}

func irTexts(t *testing.T, x *xwal.XWAL) []string {
	t.Helper()
	var first, last uint64
	for _, c := range x.Channels() {
		if c.Name == chanIR {
			first, last = c.First, c.Last
		}
	}
	var out []string
	for lt := first; lt <= last; lt++ {
		_, payload, err := x.Read(chanIR, lt)
		if err != nil {
			continue
		}
		var m struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}
		json.Unmarshal(payload, &m)
		if m.Role == "user" {
			out = append(out, m.Text)
		}
	}
	return out
}

func TestXwalStore_ForkConversation(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenXwalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := s.CreateLoadout("default", patchSet(map[string]string{"system.credo": "x"}))
	c, _ := s.CreateConversation(l)

	appendIR(t, s, c, "hello")
	appendIR(t, s, c, "world")

	cont, alt, err := s.Fork(c)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if cont == alt || cont == c || alt == c {
		t.Fatalf("fork ids not distinct: c=%s cont=%s alt=%s", c, cont, alt)
	}
	if !s.idx.Nodes[c].Frozen {
		t.Fatal("parent not frozen after fork")
	}

	// Both children see the shared prefix [hello, world].
	for _, id := range []string{cont, alt} {
		x, err := s.OpenNode(id)
		if err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
		got := irTexts(t, x)
		x.Close()
		if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
			t.Fatalf("child %s prefix = %v, want [hello world]", id, got)
		}
	}

	// Diverge: append to alt only; cont is unaffected.
	appendIR(t, s, alt, "alt-only")
	ax, _ := s.OpenNode(alt)
	agot := irTexts(t, ax)
	ax.Close()
	cx, _ := s.OpenNode(cont)
	cgot := irTexts(t, cx)
	cx.Close()
	if len(agot) != 3 || agot[2] != "alt-only" {
		t.Fatalf("alt = %v, want [hello world alt-only]", agot)
	}
	if len(cgot) != 2 {
		t.Fatalf("cont diverged unexpectedly = %v, want [hello world]", cgot)
	}

	// The frozen parent can't be re-forked via Fork (it's a node now);
	// but its children can be forked again.
	if _, _, err := s.Fork(c); err == nil {
		t.Fatal("expected fork of frozen node to fail")
	}
	appendIR(t, s, alt, "more")
	if _, _, err := s.Fork(alt); err != nil {
		t.Fatalf("fork of live child failed: %v", err)
	}
}
