package render

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func visible(lines []string) string { return stripANSI(strings.Join(lines, "\n")) }

func TestRender_Paragraph(t *testing.T) {
	r := Render("Hello, **world**.", Options{Width: 80})
	if !strings.Contains(visible(r.Lines), "Hello, world.") {
		t.Fatalf("paragraph text missing; got %q", visible(r.Lines))
	}
}

func TestRender_BashClampsToTail(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("```bash\n")
	for i := 1; i <= 200; i++ {
		sb.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	sb.WriteString("```\n")

	r := Render(sb.String(), Options{Width: 120, BashCap: 10})
	if len(r.Lines) != 11 {
		t.Fatalf("want 11 rows (header + 10), got %d:\n%s", len(r.Lines), visible(r.Lines))
	}
	if !strings.Contains(stripANSI(r.Lines[0]), "last 10 of 200 lines") {
		t.Fatalf("header wrong: %q", stripANSI(r.Lines[0]))
	}
	// Shows the tail (191..200), not the head.
	body := visible(r.Lines[1:])
	if strings.Contains(body, "line 1\n") || strings.Contains(body, "line 190") {
		t.Fatalf("expected only the tail; got:\n%s", body)
	}
	if !strings.Contains(body, "line 191") || !strings.Contains(body, "line 200") {
		t.Fatalf("tail lines missing; got:\n%s", body)
	}
}

func TestRender_BashShortNoTruncation(t *testing.T) {
	r := Render("```bash\nonly one\n```\n", Options{Width: 80})
	if !strings.Contains(stripANSI(r.Lines[0]), "bash · 1 line") {
		t.Fatalf("header wrong: %q", stripANSI(r.Lines[0]))
	}
}

func TestRender_SpinnerSentinelAnimates(t *testing.T) {
	blob := "status: " + string(SpinnerSentinel) + " running"
	t0 := visible(Render(blob, Options{Width: 80, Tick: 0}).Lines)
	t1 := visible(Render(blob, Options{Width: 80, Tick: 1}).Lines)

	if !strings.ContainsRune(t0, SpinnerFrames[0]) {
		t.Fatalf("tick 0 should show frame %q; got %q", string(SpinnerFrames[0]), t0)
	}
	if !strings.ContainsRune(t1, SpinnerFrames[1]) {
		t.Fatalf("tick 1 should show frame %q; got %q", string(SpinnerFrames[1]), t1)
	}
	if strings.ContainsRune(t0, SpinnerSentinel) {
		t.Fatal("raw sentinel leaked into output")
	}
}

func TestRender_Deterministic(t *testing.T) {
	blob := "# Title\n\nSome *prose* and a list:\n\n- a\n- b\n\n```bash\nx\ny\n```\n"
	a := Render(blob, Options{Width: 72, Tick: 3})
	b := Render(blob, Options{Width: 72, Tick: 3})
	if strings.Join(a.Lines, "\n") != strings.Join(b.Lines, "\n") {
		t.Fatal("Render is not deterministic for identical inputs")
	}
}

func TestRender_Table(t *testing.T) {
	md := "| col | val |\n| --- | --- |\n| a | 1 |\n| b | 2 |\n"
	out := visible(Render(md, Options{Width: 80}).Lines)
	for _, want := range []string{"col", "val", "a", "b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table cell %q missing; got:\n%s", want, out)
		}
	}
}

func TestRender_IncompleteTrailingBashFence(t *testing.T) {
	// No closing ``` — streaming tool output. Must render the body, not panic.
	r := Render("```bash\nstreaming line 1\nstreaming line 2\n", Options{Width: 80})
	out := visible(r.Lines)
	if !strings.Contains(out, "streaming line 2") {
		t.Fatalf("streaming bash body missing; got:\n%s", out)
	}
}

func TestRender_BashLineWraps(t *testing.T) {
	long := strings.Repeat("x", 30)
	r := Render("```bash\n"+long+"\n```\n", Options{Width: 10})
	// header + ceil(30/10)=3 rows.
	if len(r.Lines) != 4 {
		t.Fatalf("want header + 3 wrapped rows, got %d:\n%s", len(r.Lines), visible(r.Lines))
	}
	for _, row := range r.Lines[1:] {
		if w := len(stripANSI(row)); w > 10 {
			t.Fatalf("row exceeds width: %q (%d cols)", row, w)
		}
	}
}

func TestRender_Empty(t *testing.T) {
	r := Render("", Options{Width: 80})
	if len(r.Lines) != 0 {
		t.Fatalf("empty blob should render no rows, got %d", len(r.Lines))
	}
}
