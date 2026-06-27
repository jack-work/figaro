package render

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/doc"
)

// harness wires a Doc + Renderer + FakeTerminal and renders after every event.
type harness struct {
	ft *FakeTerminal
	d  *doc.Doc
	r  *Renderer
}

func newHarness(w, h int) *harness {
	ft := NewFakeTerminal(w, h)
	return &harness{ft: ft, d: doc.New(), r: New(ft, TextRenderer{})}
}

func (h *harness) apply(e doc.Event) { h.d.Apply(e); h.r.Render(h.d.Blocks()) }

func (h *harness) body(id, old, new string) {
	delta, _ := doc.Diff(old, new)
	h.apply(doc.Patch(id, delta))
}

func (h *harness) screen() string { return strings.Join(h.ft.Screen(), "\n") }

var spinners = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func hasSpinner(s string) bool { return strings.ContainsAny(s, spinners) }

func count(s, sub string) int { return strings.Count(s, sub) }

func TestRender_StreamingGrowsNoDup(t *testing.T) {
	h := newHarness(40, 20)
	h.apply(doc.Append(doc.Block{ID: "a", Kind: "text", Status: doc.StatusActive}))
	prev := ""
	for _, s := range []string{"He", "Hello", "Hello wor", "Hello world"} {
		h.body("a", prev, s)
		prev = s
	}
	h.apply(doc.SetStatus("a", doc.StatusOK))
	scr := h.screen()
	if count(scr, "Hello world") != 1 {
		t.Fatalf("body should appear once:\n%s", scr)
	}
	if hasSpinner(scr) {
		t.Fatalf("no spinner after completion:\n%s", scr)
	}
}

func TestRender_ParallelToolsNoBleed(t *testing.T) {
	h := newHarness(60, 30)
	for _, id := range []string{"x", "y", "z"} {
		h.apply(doc.Append(doc.Block{ID: id, Kind: "tool", Status: doc.StatusActive,
			Attrs: map[string]string{"title": id + ".go"}}))
	}
	// outputs arrive interleaved
	h.body("x", "", "wrote x")
	h.body("z", "", "wrote z")
	h.body("y", "", "wrote y")
	h.apply(doc.SetStatus("x", doc.StatusOK))
	h.apply(doc.SetStatus("y", doc.StatusOK))
	h.apply(doc.SetStatus("z", doc.StatusOK))
	scr := h.screen()
	for _, id := range []string{"x.go", "y.go", "z.go"} {
		if count(scr, id) != 1 {
			t.Errorf("header %s should appear once:\n%s", id, scr)
		}
	}
	// each tool's body sits under its own header
	if !strings.Contains(scr, "wrote x") || !strings.Contains(scr, "wrote y") || !strings.Contains(scr, "wrote z") {
		t.Errorf("missing bodies:\n%s", scr)
	}
}

// The figaro killer: a viewport height shrink mid-turn while a tool is still
// running. With a screen-owning pi renderer this resolves to a clean full
// redraw — no duplicated header, no stranded spinner.
func TestRender_ResizeMidStreamWhileRunning(t *testing.T) {
	h := newHarness(70, 24)
	h.apply(doc.Append(doc.Block{ID: "th", Kind: "thinking"}))
	h.body("th", "", "I'll write the script, chmod it, then cat it to check.")
	h.apply(doc.Append(doc.Block{ID: "w", Kind: "write", Status: doc.StatusOK,
		Attrs: map[string]string{"title": "S.sh"}}))
	h.body("w", "", "Wrote 196 bytes to S.sh")
	h.apply(doc.Append(doc.Block{ID: "b", Kind: "bash", Status: doc.StatusActive,
		Attrs: map[string]string{"title": "chmod && cat"}}))
	h.body("b", "", "#!/usr/bin/env bash\nline1\nline2\nline3\nline4\nline5\nline6")

	// SIGWINCH: the pane shrinks (non-focused/relaid-out) while bash is running.
	h.ft.Resize(70, 8)
	h.r.Render(h.d.Blocks())

	// bash completes after the resize
	h.apply(doc.SetStatus("b", doc.StatusOK))

	scr := h.screen()
	if count(scr, "bash chmod && cat") != 1 {
		t.Fatalf("bash header duplicated after resize:\n%s", scr)
	}
	if hasSpinner(scr) {
		t.Fatalf("stranded spinner after resize+complete:\n%s", scr)
	}
	if count(scr, "write S.sh") != 1 || count(scr, "thinking") != 1 {
		t.Fatalf("other blocks duplicated after resize:\n%s", scr)
	}
}

func TestRender_TickAnimatesActiveOnly(t *testing.T) {
	h := newHarness(40, 20)
	h.apply(doc.Append(doc.Block{ID: "a", Kind: "tool", Status: doc.StatusActive,
		Attrs: map[string]string{"title": "run"}}))
	h.r.Tick()
	h.r.Tick()
	if !hasSpinner(h.screen()) {
		t.Fatalf("active block should show a spinner:\n%s", h.screen())
	}
	h.apply(doc.SetStatus("a", doc.StatusOK))
	h.r.Tick() // tick after completion must not resurrect a spinner
	if hasSpinner(h.screen()) {
		t.Fatalf("completed block should not spin:\n%s", h.screen())
	}
}
