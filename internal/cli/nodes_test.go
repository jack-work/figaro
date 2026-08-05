package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
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
	rows := renderToolNode(n, 80, 5, 0, false, false) // bashCap=5
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

	rows := renderToolNode(n, 120, 5, 0, true, true)
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
	rendered := strings.Join(renderToolNode(n, 80, 2, 0, false, false), "\n")
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

// A running tool draws its arguments as they arrive: a short value beside its
// label, a long one beneath it, all under the argument gutter — which is a
// different rule, in a different colour, from the one tool OUTPUT uses, so a
// dense transcript says at a glance what the agent asked for and what the
// command printed. Nothing here knows what a "write" or a "bash" is.
func TestRenderToolNode_StreamingInput(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/var/tmp/x.md","content":"1. alpha\n2. beta`,
	}
	want := []string{
		"⠋ write",
		"  │ content",
		"  │   1. alpha",
		"  │   2. beta",
		"  │ path    /var/tmp/x.md",
	}
	assertRows(t, renderNodeRows(t, n, 44, 10, false), want)
}

// Folded, ONE argument shows its last argStreamLines rows — a moving window
// on what is being typed, not a summary of it. No "… last N of M" banner: the
// count changes every frame, which is noise rather than information.
func TestRenderToolNode_StreamingInputIsAMovingWindow(t *testing.T) {
	var body strings.Builder
	for i := range 40 {
		fmt.Fprintf(&body, "%d. a line of the file being written\\n", i)
	}
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x","content":"` + body.String(),
	}
	rows := renderNodeRows(t, n, 60, 10, false)
	// header + content label + argStreamLines value rows + the path row
	if len(rows) != 3+argStreamLines {
		t.Fatalf("folded stream should be %d rows, got %d:\n%s", 3+argStreamLines, len(rows), strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[len(rows)-2], "39.") {
		t.Errorf("the window should hold the NEWEST lines, got %q", rows[len(rows)-1])
	}
	for _, r := range rows {
		if strings.Contains(r, "last") && strings.Contains(r, "lines") {
			t.Errorf("no truncation banner belongs on a streaming argument: %q", r)
		}
	}
	// Expanded, every line is there.
	if got := renderNodeRows(t, n, 60, nodeOutputUnlimited, true); len(got) <= 3+argStreamLines {
		t.Fatalf("expanded should reveal the whole value, got %d rows", len(got))
	}
}

// A settled tool keeps its arguments, folded to the FIRST argSettledLines rows
// of each value: nothing is moving any more, so the useful part is the head —
// what this call is — not the tail, which is only where it stopped. The header
// drops its summary once the block is drawn, because the block already carries
// the command in full, and two copies of it are what pushed the duration off
// the right of the screen.
func TestRenderToolNode_SettledArgsPreviewFromTheHead(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Summary: "git push", Args: map[string]any{"command": "git push origin main", "timeout": 240},
		Output: "everything up-to-date",
	}
	rows := renderNodeRows(t, n, 60, nodeBashCapDefault, false)
	assertRows(t, rows[:3], []string{
		"✓ bash",
		"  │ command git push origin main",
		"  │ timeout 240",
	})
}

// The gesture must not be inert on the node you most want to open: a running
// tool has no output yet, and its arguments are the whole story.
func TestNodeExpandable_StreamingToolWithNoOutput(t *testing.T) {
	streaming := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x","content":"a`}
	if !nodeExpandable(streaming) {
		t.Error("a streaming tool must be expandable — its arguments are what there is to see")
	}
	settled := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": "ls"}}
	if !nodeExpandable(settled) {
		t.Error("a settled tool hides its arguments, so it has something to reveal")
	}
	if nodeExpandable(livedoc.Node{Type: livedoc.NodeTool, Name: "bash"}) {
		t.Error("a tool with neither arguments nor output reveals nothing")
	}
}

// Arguments are drawn in their own colour, which is what tells them from tool
// output now that both use the same rule. Vacuous when colour is off (term.Arg
// is the identity then) and load-bearing when it is on.
func TestRenderToolNode_ArgumentsAreDrawnInTheArgumentColour(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x.md"}`}
	joined := strings.Join(renderToolNode(n, 60, 10, 0, false, false), "\n")
	if !strings.Contains(joined, term.Arg("/x.md")) {
		t.Errorf("argument value is not drawn through term.Arg:\n%q", joined)
	}
	if !strings.Contains(joined, term.Arg(argGutter)) {
		t.Errorf("argument rule is not drawn through term.Arg:\n%q", joined)
	}
}

func assertRows(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// renderNodeRows renders a node and strips styling, so assertions are about
// layout rather than escape codes.
func renderNodeRows(t *testing.T, n livedoc.Node, width, cap int, expand bool) []string {
	t.Helper()
	raw := renderToolNode(n, width, cap, 0, false, expand)
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = stripANSI(r)
	}
	return out
}

// The INCIPIT must not expand arguments, and the reason is not obvious: the
// shared Composer treats a nil Expanded map as "this surface has no gesture,
// so draw the fullest form", which is right for output (inline rows freeze to
// scrollback and can never be re-rendered) and wrong for a streaming argument,
// whose whole value is that it stays a small moving window until asked.
//
// Driven through the real Composer rather than the view, because the nil-map
// default lives there and that is the thing being pinned.
func TestIncipitDrawsArgumentsFoldedButOutputWhole(t *testing.T) {
	body := strings.Repeat("a line of the file being written\\n", 20)
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input:  `{"path":"/x.md","content":"` + body,
		Output: strings.TrimRight(strings.Repeat("output line\n", 40), "\n"),
	}
	// Argument rows and output rows share the same RULE — that is the point of
	// the change, and colour carries the distinction (term.Arg), which a test
	// cannot force here because the colour mode is resolved once at init. So
	// the two are counted on fixtures that have only one of them.
	ruleRows := func(view ldrender.NodeView, expanded func(int) bool, node livedoc.Node) int {
		c := ldrender.Composer{View: view, Tick: 0, Expanded: expanded}
		count := 0
		for _, r := range c.Nodes([]livedoc.Node{node}, 70) {
			if strings.Contains(stripANSI(r.Text), "│") {
				count++
			}
		}
		return count
	}
	argsOnly := n
	argsOnly.Output = ""
	outOnly := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK, Output: n.Output}
	rowsOf := func(view ldrender.NodeView, expanded func(int) bool) (args, out int) {
		return ruleRows(view, expanded, argsOnly), ruleRows(view, expanded, outOnly)
	}

	// Expanded nil is the incipit: no gesture, "draw the fullest form".
	args, out := rowsOf(&ariaView{settings: &renderSettings{}}, nil)
	if args != 2+argStreamLines {
		t.Errorf("incipit arguments: %d rows, want %d (the moving window)", args, 2+argStreamLines)
	}
	if out != 40 {
		t.Errorf("incipit output: %d rows, want all 40 — inline never collapses what it cannot reopen", out)
	}

	// The pager always states the per-node answer, and an unexpanded node
	// gets the same window — one shape, two surfaces.
	unexpanded := func(int) bool { return false }
	pagerArgs, pagerOut := rowsOf(pagerView(&ariaView{settings: &renderSettings{}}), unexpanded)
	if pagerArgs != 2+argStreamLines {
		t.Errorf("pager unexpanded arguments: %d rows, want %d", pagerArgs, 2+argStreamLines)
	}
	if pagerOut != nodeBashCapDefault+1 { // + the "… last N of M lines" note
		t.Errorf("pager unexpanded output: %d rows, want %d — the pager DOES collapse output", pagerOut, nodeBashCapDefault+1)
	}
}
