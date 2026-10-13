package cli

import (
	"github.com/mattn/go-runewidth"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// stripANSI uses escapeEnd, the package's one escape grammar.
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

func TestRenderToolNode_RunningOutputClampedToBashCap(t *testing.T) {
	// The visible body is tail-clamped to bashCap: earlier lines must not leak.
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "line"+string(rune('A'+i%26)))
	}

	lines[0] = "EARLY_LEAK_SENTINEL"
	lines[len(lines)-1] = "LATE_TAIL_SENTINEL"
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning,
		Summary: "long", Output: strings.Join(lines, "\n"),
	}
	rows := renderToolNode(n, 80, 5, 0, false)
	joined := stripANSI(strings.Join(rows, "\n"))
	if strings.Contains(joined, "EARLY_LEAK_SENTINEL") {
		t.Errorf("early output must be clamped, but leaked:\n%s", joined)
	}
	if !strings.Contains(joined, "LATE_TAIL_SENTINEL") {
		t.Errorf("late tail should be visible:\n%s", joined)
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

// The question that opened a turn reads as the user's voice in the inline
// renderer, the pager and `show` alike.
func TestInquiryDrawsAsTheUsersVoiceInEveryView(t *testing.T) {
	reply := livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "RED"}
	steer := livedoc.Node{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "actually, whisper it"}

	// Both voices, each under its own header, the question first.
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
		t.Fatalf("the inquiry wore steering's marker: the duplicate-voice bug:\n%s", turn)
	}

	// "↳ input" is reserved for steering, the one input-voice node.
	view := &ariaView{settings: &renderSettings{}}
	if !strings.Contains(stripANSI(strings.Join(view.Render(steer, 60, 0), "\n")), "↳ input") {
		t.Fatal("steering lost its marker")
	}
	if a := stripANSI(strings.Join(view.Render(reply, 60, 0), "\n")); strings.Contains(a, "↳ input") {
		t.Fatalf("output prose drew the user's marker: %q", a)
	}
}

// The duration is on screen at every width.
func TestRenderToolNode_DurationSurvivesEveryWidth(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Summary:   strings.Repeat("a very long command line ", 20),
		Args:      map[string]any{"command": strings.Repeat("a very long command line ", 20)},
		Output:    "done",
		StartedAt: 1785862036094, FinishedAt: 1785862097094,
	}
	want := "[1m01s]"
	for w := 20; w <= 200; w++ {
		for _, expand := range []bool{false, true} {
			rows := renderNodeRows(t, n, w, 10, expand)
			if !strings.Contains(rows[0], want) {
				t.Fatalf("width %d expand=%v: header %q lost the duration", w, expand, rows[0])
			}
			for i, r := range rows {
				if got := runewidth.StringWidth(r); got > w {
					t.Fatalf("width %d expand=%v: row %d is %d cells: %q", w, expand, i, got, r)
				}
			}
		}
	}
}

// A running tool has no output yet; its arguments must keep the gesture live.
func TestNodeExpandable_StreamingToolWithNoOutput(t *testing.T) {
	streaming := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x","content":"a`}
	if !nodeExpandable(streaming) {
		t.Error("a streaming tool must be expandable: its arguments are what there is to see")
	}
	settled := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": "ls"}}
	if !nodeExpandable(settled) {
		t.Error("a settled tool folds its arguments, so it has something to reveal")
	}
	if nodeExpandable(livedoc.Node{Type: livedoc.NodeTool, Name: "bash"}) {
		t.Error("a tool with neither arguments nor output reveals nothing")
	}
}

// The call (name and headline argument) is one colour; the rule is the same dim
// the output rule uses.
func TestRenderToolNode_ColoursAreConsistent(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args: map[string]any{"path": "/x.md", "content": "a line"}}
	joined := strings.Join(renderToolNode(n, 60, 10, 0, false), "\n")
	// A golden records the layout; only a test can say what colour a run carries.
	for _, want := range []string{term.Arg("write"), term.Body("/x.md"), term.Dim(toolGutter)} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%q", want, joined)
		}
	}
}

// Diff painting is declared, never inferred from diff-shaped output.
func TestRenderToolNode_DiffRowsArePainted(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	out := "-1 was\n+1 is"
	edit := livedoc.Node{Type: livedoc.NodeTool, Name: "edit", Status: livedoc.StatusOK,
		Args: map[string]any{"path": "/x.py"}, Output: out}
	joined := strings.Join(renderToolNode(edit, 60, 10, 0, false), "\n")
	for _, want := range []string{term.DiffDel("-1 was"), term.DiffAdd("+1 is")} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%q", want, joined)
		}
	}

	sh := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": "git diff"}, Output: out}
	if strings.Contains(strings.Join(renderToolNode(sh, 60, 10, 0, false), "\n"), term.DiffAdd("+1 is")) {
		t.Error("a shell's output was painted as a diff")
	}
}

func boxContentText(plain string) string {
	i := strings.Index(plain, "│")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(plain[i+len("│"):])
}

func renderNodeRows(t *testing.T, n livedoc.Node, width, cap int, expand bool) []string {
	t.Helper()
	raw := renderToolNode(n, width, cap, 0, expand)
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = stripANSI(r)
	}
	return out
}

// A rewritten row is not a clipped row: tab-indented source fails clipFits however
// short, and must not earn an ellipsis.
func TestClipToWidthEllipsis_NoPhantomOnRewrittenRow(t *testing.T) {
	for _, s := range []string{"\tshort", " 50 \tOpenedAt: o}", "a\tb\tc"} {
		if got := clipToWidthEllipsis(s, 60); strings.HasSuffix(stripANSI(got), "…") {
			t.Errorf("clipToWidthEllipsis(%q, 60) = %q; nothing was dropped", s, stripANSI(got))
		}
	}
	if got := clipToWidthEllipsis("\tabcdefghij", 6); !strings.HasSuffix(stripANSI(got), "…") {
		t.Errorf("clipToWidthEllipsis = %q; want an ellipsis, text was dropped", stripANSI(got))
	}
}

// Any tool can declare a diff by fencing it; the painter is the edit tool's.
func TestToolOutputFencedDiffDrawsAsADiff(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	out := "$ git diff --stat\n```diff\n--- a/aria.go\n+++ b/aria.go\n@@ -1,2 +1,2 @@\n-was\n+is\n```\n1 file changed"
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": "git diff"}, Output: out}
	rows := renderToolNode(n, 80, nodeOutputUnlimited, 0, true)

	add, del := sgrOf(term.DiffAdd), sgrOf(term.DiffDel)
	var painted, cut, bare bool
	for _, r := range rows {
		switch strings.TrimSpace(stripANSI(strings.TrimPrefix(stripANSI(r), toolGutter))) {
		case "+is":
			painted = strings.Contains(r, add)
		case "-was":
			cut = strings.Contains(r, del)
		case "1 file changed":
			bare = !strings.Contains(r, add) && !strings.Contains(r, del)
		}
	}
	if !painted || !cut || !bare {
		t.Fatalf("fenced diff not painted (add=%v del=%v plain=%v):\n%s", painted, cut, bare,
			strings.Join(plain2(rows), "\n"))
	}
	if joined := strings.Join(plain2(rows), "\n"); strings.Contains(joined, "```") {
		t.Fatalf("the fence reached the screen:\n%s", joined)
	}
}

// A `write` body is a file: fences in it are the file's own text.
func TestWriteBodyKeepsItsFences(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args:   map[string]any{"path": "/tmp/notes.md", "content": "# notes\n```diff\n-a\n+b\n```"},
		Output: "/tmp/notes.md"}
	rows := plain2(renderToolNode(n, 80, nodeOutputUnlimited, 0, false))
	if !strings.Contains(strings.Join(rows, "\n"), "```diff") {
		t.Fatalf("a written file lost its fence:\n%s", strings.Join(rows, "\n"))
	}
}

func sgrOf(paint func(string) string) string {
	s := paint("\x00")
	return s[:strings.IndexByte(s, 0)]
}

func plain2(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = stripANSI(r)
	}
	return out
}
