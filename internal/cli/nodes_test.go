package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// stripANSI removes ANSI escape sequences so tests can assert on visible text.
// It uses escapeEnd — the package's ONE escape grammar (b568b6f) — rather than
// a fourth hand-rolled scanner; farmerStrip was a fifth, and is gone.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i, _ = escapeEnd(s, i)
			continue
		}
		b.WriteByte(s[i])
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
	rows := renderNodeList(nodes, 80, 0, renderSettings{})
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

// TestInquiryDrawsAsTheUsersVoiceInEveryView pins the fix for a regression found
// in final validation: the question that opened a turn must read as the user's
// voice in the inline renderer, the pager and `show` alike.
//
// The MECHANISM changed twice. It was an inline "↳ input" marker on a prompt
// NODE, because `show` had no header to carry a voice — but that marker belongs
// to STEERING, so the prompt wearing it printed the voice twice in incipit and
// mislabelled the prompt as steering in `show`. Then it was a voice-run header
// over node 0. Now the question is not a node at all: it is Turn.Inquiry, drawn
// by inquiryRows under the same "> input" header every view uses.
func TestInquiryDrawsAsTheUsersVoiceInEveryView(t *testing.T) {
	reply := livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "RED"}
	steer := livedoc.Node{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "actually, whisper it"}

	// A whole turn shows BOTH voices, each under its own header, in order —
	// the question first, though it is text rather than a node.
	turn := stripANSI(strings.Join(
		renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "what is the codeword?", InquirySegments: nil, Nodes: []livedoc.Node{reply}}, 60, 0, renderSettings{}), "\n"))
	ui, fi := strings.Index(turn, "> input"), strings.Index(turn, "< figaro")
	if ui < 0 || fi < 0 || ui > fi {
		t.Fatalf("turn must head the inquiry then the output run:\n%s", turn)
	}
	if !strings.Contains(turn, "what is the codeword?") {
		t.Fatalf("the inquiry text is missing:\n%s", turn)
	}
	if strings.Contains(turn, "↳ input") {
		t.Fatalf("the inquiry wore steering's marker — the duplicate-voice bug:\n%s", turn)
	}

	// "↳ input" is reserved for genuine steering — the one input-voice NODE.
	view := &ariaView{settings: &renderSettings{}}
	if !strings.Contains(stripANSI(strings.Join(view.Render(steer, 60, 0), "\n")), "↳ input") {
		t.Fatal("steering lost its marker")
	}
	if a := stripANSI(strings.Join(view.Render(reply, 60, 0), "\n")); strings.Contains(a, "↳ input") {
		t.Fatalf("output prose drew the user's marker: %q", a)
	}
}

// A running tool draws its arguments as they arrive: each field's label on its
// own line, the value beneath and indented one step further, so a wrapped
// value cannot be misread as the next argument. Nothing here knows what a
// "write" or a "bash" is — the block is walked out of the partial JSON.
func TestRenderToolNode_StreamingInput(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/var/tmp/x.md","content":"1. alpha\n2. beta`,
	}
	rows := renderNodeRows(t, n, 40, 10)
	want := []string{
		"⠋ write",
		"  path",
		"    /var/tmp/x.md",
		"  content",
		"    1. alpha",
		"    2. beta",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d:\n%s", len(rows), len(want), strings.Join(rows, "\n"))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: got %q, want %q", i, rows[i], want[i])
		}
	}
}

// The block is tail-clamped like tool output: a 4 KB argument may not push the
// rest of the conversation off the screen.
func TestRenderToolNode_StreamingInputIsBounded(t *testing.T) {
	var body strings.Builder
	for i := range 200 {
		fmt.Fprintf(&body, "%d. a line of the file being written\\n", i)
	}
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x","content":"` + body.String(),
	}
	if rows := renderNodeRows(t, n, 60, 10); len(rows) != 1+10 {
		t.Fatalf("want header + 10 clamped rows, got %d", len(rows))
	}
}

// Once the decoded Args land the streaming block is gone: compose clears
// Input, and the header's summary says the same thing in one line.
func TestRenderToolNode_NoInputBlockOnceArgsLand(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Summary: "ls -la", Args: map[string]any{"command": "ls -la"},
	}
	for _, r := range renderNodeRows(t, n, 40, 10) {
		if strings.HasPrefix(r, "  command") {
			t.Fatalf("input block survived Args landing:\n%s", r)
		}
	}
}

// renderNodeRows renders a node and strips styling, so assertions are about
// layout rather than escape codes.
func renderNodeRows(t *testing.T, n livedoc.Node, width, cap int) []string {
	t.Helper()
	raw := renderToolNode(n, width, cap, 0, false)
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = stripANSI(r)
	}
	return out
}
