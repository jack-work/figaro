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
		// Skip the loadout node's empty-content birth tic (RoleUser with
		// no text); it carries the loadout transition via the chalkboard
		// channel, not IR text.
		if m.Role == "user" && m.Text != "" {
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

func TestXwalStore_ForkAt(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenXwalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := s.CreateLoadout("default", patchSet(map[string]string{"system.credo": "x"}))
	c, _ := s.CreateConversation(l)
	appendIR(t, s, c, "a")
	appendIR(t, s, c, "b")
	appendIR(t, s, c, "c")

	// Find the LT of "c" (the tail) and fork there: history below it
	// ([a, b]) is shared; the suffix [c] becomes the continuation.
	x, _ := s.OpenNode(c)
	tail := irLast(x)
	x.Close()

	cont, alt, err := s.ForkAt(c, tail)
	if err != nil {
		t.Fatalf("forkAt: %v", err)
	}
	if !s.idx.Nodes[c].Frozen {
		t.Fatal("parent not frozen after interior fork")
	}

	cx, _ := s.OpenNode(cont)
	cg := irTexts(t, cx)
	cx.Close()
	ax, _ := s.OpenNode(alt)
	ag := irTexts(t, ax)
	ax.Close()

	// Continuation keeps the full line [a, b, c]; the alternative shares
	// the frozen prefix [a, b] only.
	if len(cg) != 3 || cg[2] != "c" {
		t.Fatalf("continuation = %v, want [a b c]", cg)
	}
	if len(ag) != 2 || ag[0] != "a" || ag[1] != "b" {
		t.Fatalf("alternative = %v, want [a b]", ag)
	}
	// Continuation inherits the trunk; alternative founds its own.
	if s.idx.Nodes[cont].Trunk != s.idx.Nodes[c].Trunk {
		t.Fatal("continuation should inherit the trunk")
	}
	if s.idx.Nodes[alt].Trunk != alt {
		t.Fatal("alternative should found its own trunk")
	}
}
