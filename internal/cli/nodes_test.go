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

func TestTailOutput(t *testing.T) {
	output := "one\ntwo\nthree\nfour"
	if got, total := tailOutput(output, 2); got != "three\nfour" || total != 4 {
		t.Fatalf("tailOutput = %q, %d", got, total)
	}
	if got, total := tailOutput(output, 0); got != "" || total != 4 {
		t.Fatalf("zero tailOutput = %q, %d", got, total)
	}
	if got, total := tailOutput(output, nodeOutputUnlimited); got != output || total != 4 {
		t.Fatalf("unlimited tailOutput = %q, %d", got, total)
	}
}

func TestRenderToolNodeSanitizesBeforeTailClamp(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Output: "\x1b]2;hidden\nOSC payload\nmore payload\x07\nvisible",
	}
	rendered := strings.Join(renderToolNode(n, 80, 2, 0, false), "\n")
	if strings.Contains(rendered, "OSC payload") || strings.ContainsRune(rendered, '\a') {
		t.Fatalf("control-string payload leaked after tail clamp: %q", rendered)
	}
	if !strings.Contains(rendered, "visible") {
		t.Fatalf("visible tail missing: %q", rendered)
	}
}

// TestPromptDrawsAsTheUsersVoiceInEveryView pins the fix for a regression found
// in final validation: a turn's prompt is node 0 with Role "input", and it must
// read as the user's voice in the inline renderer, the pager and `show` alike.
//
// The MECHANISM changed in S33. It used to be an inline "↳ you" marker on the
// node, because `show` had no header to carry a voice. That marker belongs to
// STEERING, so the prompt wearing it printed the voice twice in incipit (once as
// the run header, once as the marker) and mislabelled the prompt as steering in
// `show`. Now every view derives the voice from the same contiguous-run header
// (aria.VoiceRunEnd + messageHeader), and the node itself draws as plain prose.
func TestPromptDrawsAsTheUsersVoiceInEveryView(t *testing.T) {
	prompt := livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleInput, Markdown: "what is the codeword?"}
	reply := livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "RED"}
	steer := livedoc.Node{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "actually, whisper it"}

	// A whole turn, rendered by the shared walker, shows BOTH voices, each
	// under its own header, in order.
	turn := stripANSI(strings.Join(
		renderTurnRows([]livedoc.Node{prompt, reply}, 60, 0, 0, renderSettings{}), "\n"))
	ui, fi := strings.Index(turn, "❯ you"), strings.Index(turn, "‹ figaro")
	if ui < 0 || fi < 0 || ui > fi {
		t.Fatalf("turn must head the input run then the output run:\n%s", turn)
	}
	if strings.Contains(turn, "↳ you") {
		t.Fatalf("the inquiry wore steering's marker — the duplicate-voice bug:\n%s", turn)
	}

	// One dispatch: the node itself renders identically wherever it is drawn.
	view := &ariaView{settings: &renderSettings{}}
	got := stripANSI(strings.Join(view.Render(prompt, 60, 0), "\n"))
	viaList := stripANSI(strings.Join(renderNodeList([]livedoc.Node{prompt}, 60, 0, 0, renderSettings{}), "\n"))
	if strings.TrimSpace(got) != strings.TrimSpace(viaList) {
		t.Fatalf("views disagree on one node:\n ariaView: %q\n show:     %q", got, viaList)
	}

	// "↳ you" is reserved for genuine steering — not the inquiry, not output.
	if !strings.Contains(stripANSI(strings.Join(view.Render(steer, 60, 0), "\n")), "↳ you") {
		t.Fatal("steering lost its marker")
	}
	if strings.Contains(got, "↳ you") {
		t.Fatalf("the inquiry drew steering's marker: %q", got)
	}
	if a := stripANSI(strings.Join(view.Render(reply, 60, 0), "\n")); strings.Contains(a, "↳ you") {
		t.Fatalf("output prose drew the user's marker: %q", a)
	}
}
