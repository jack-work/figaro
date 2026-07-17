package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// stripANSI removes ANSI escape sequences so tests can assert on visible text.
func stripANSI(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' {
			j := i + 1
			for j < len(rs) && !((rs[j] >= 'A' && rs[j] <= 'Z') || (rs[j] >= 'a' && rs[j] <= 'z')) {
				j++
			}
			if j < len(rs) {
				j++
			}
			i = j
			continue
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

func TestRenderToolNode_UniformAcrossTools(t *testing.T) {
	// bash, write, and an unknown tool ALL render as: glyph name summary.
	// No per-tool code path — same shape, same code.
	nodes := []livedoc.Node{
		{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK, Summary: "ls -la"},
		{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK, Summary: "/tmp/a"},
		{Type: livedoc.NodeTool, Name: "mystery", Status: livedoc.StatusOK, Summary: "k=v"},
	}
	rows := renderNodeList(nodes, 80, 10, 0, renderSettings{})
	if len(rows) < 5 {
		t.Fatalf("want at least 5 rows (3 headers + 2 separators), got %d: %v", len(rows), rows)
	}
	// The three headers land at rows[0], rows[2], rows[4] (blank between).
	want := []struct{ name, summary string }{
		{"bash", "ls -la"},
		{"write", "/tmp/a"},
		{"mystery", "k=v"},
	}
	for i, w := range want {
		got := stripANSI(rows[i*2])
		if !strings.Contains(got, w.name) || !strings.Contains(got, w.summary) {
			t.Errorf("row %d: want name=%q summary=%q, got %q", i*2, w.name, w.summary, got)
		}
	}
	// Separator blanks in between.
	if rows[1] != "" || rows[3] != "" {
		t.Errorf("want blank separators at 1,3: %q %q", rows[1], rows[3])
	}
}

func TestRenderToolNode_RunningOutputClampedToBashCap(t *testing.T) {
	// A running tool whose Output has many lines: the visible body is
	// tail-clamped to bashCap — earlier lines must not leak.
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "line"+string(rune('A'+i%26)))
	}

	// Give distinct sentinel content early vs late.
	lines[0] = "EARLY_LEAK_SENTINEL"
	lines[len(lines)-1] = "LATE_TAIL_SENTINEL"
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning,
		Summary: "long", Output: strings.Join(lines, "\n"),
	}
	rows := renderToolNode(n, 80, 5, 0, false) // bashCap=5
	joined := stripANSI(strings.Join(rows, "\n"))
	if strings.Contains(joined, "EARLY_LEAK_SENTINEL") {
		t.Errorf("early output must be clamped, but leaked:\n%s", joined)
	}
	if !strings.Contains(joined, "LATE_TAIL_SENTINEL") {
		t.Errorf("late tail should be visible:\n%s", joined)
	}
}

func TestRenderToolNode_TimingAndVerboseDetails(t *testing.T) {
	n := livedoc.Node{
		Type:       livedoc.NodeTool,
		Name:       "bash",
		Status:     livedoc.StatusOK,
		StartedAt:  1_700_000_000_000,
		FinishedAt: 1_700_000_001_250,
	}
	rows := renderToolNode(n, 120, 5, 0, true)
	joined := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(joined, "[1.2s]") {
		t.Fatalf("duration missing: %s", joined)
	}
	if !strings.Contains(joined, "started ") || !strings.Contains(joined, "finished ") {
		t.Fatalf("verbose timestamps missing: %s", joined)
	}
}
