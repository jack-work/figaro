package cli

import (
	"fmt"
	"github.com/mattn/go-runewidth"
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
	// The renderer has zero per-tool control flow: it reads Name and the
	// argument fields and nothing else. Three tools, one shape.
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"bash", map[string]any{"command": "ls -la"}, "command ls -la"},
		{"write", map[string]any{"path": "/tmp/a"}, "path /tmp/a"},
		{"mystery", map[string]any{"k": "v"}, "k v"},
	} {
		n := livedoc.Node{Type: livedoc.NodeTool, Name: tc.name, Status: livedoc.StatusOK, Args: tc.args}
		rows := renderNodeRows(t, n, 60, 10, false)
		if strings.TrimSpace(rows[0]) != "✓ "+tc.name && !strings.HasPrefix(rows[0], "✓ "+tc.name+" ") {
			t.Errorf("%s: header = %q", tc.name, rows[0])
		}
		if !strings.Contains(strings.Join(rows, "\n"), tc.want) {
			t.Errorf("%s: block does not carry %q:\n%s", tc.name, tc.want, strings.Join(rows, "\n"))
		}
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

// The box, in the shape the owner's fourth round specifies: a top edge the
// header sits on, air, the call, air, a labelled junction, air, the result,
// air, and a floor. The floor and the junction arrive WITH the result — until
// the tool has run there is nothing to divide and nothing to close under.
func TestRenderToolNode_BoxShape(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args:   map[string]any{"path": "/x.md"},
		Output: "Wrote 5 bytes", StartedAt: 1785862036094, FinishedAt: 1785862036098,
	}
	// A left gutter and two labels, and no rules at all: a horizontal bar
	// across the screen for every tool call made the transcript look ruled
	// rather than written. The block ends where its output ends — the turn
	// already puts a blank line between nodes.
	// The block is fitted to its content, not to the pane.
	assertRows(t, renderNodeRows(t, n, 44, 10, false), []string{
		"✓ write [4ms]",
		"  │ ",
		"  │ path /x.md",
		"  │ ",
		"✓ done [4ms]",
		"  │ ",
		"  │ Wrote 5 bytes",
	})
}

// While the arguments are still arriving there is no junction and no floor:
// the box is open at the bottom, because closing it early would mean
// reopening it when the result lands.
func TestRenderToolNode_NoJunctionUntilItRuns(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x.md"}`}
	rows := renderNodeRows(t, n, 44, nodeOutputUnlimited, false)
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "done") || strings.Contains(joined, "running") {
		t.Errorf("no junction until the tool has run:\n%s", joined)
	}
	if !strings.HasPrefix(rows[0], "⠋ write") {
		t.Errorf("the header must name the call: %q", rows[0])
	}
}

// Folded rows ELLIPSISE; expanded rows WRAP. That is the whole difference
// between the two states, and it is why a folded row never needs a wrap.
func TestRenderToolNode_FoldedEllipsisesExpandedWraps(t *testing.T) {
	long := "a single very long line of argument text that will not fit inside a narrow box at all"
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args: map[string]any{"content": long + "\nsecond line"}}
	folded := strings.Join(renderNodeRows(t, n, 50, 10, false), "\n")
	if !strings.Contains(folded, "…") {
		t.Errorf("folded overflow should be occluded by an ellipsis:\n%s", folded)
	}
	expanded := renderNodeRows(t, n, 50, nodeOutputUnlimited, true)
	var joined string
	for _, r := range expanded {
		joined += strings.TrimSpace(strings.Trim(strings.TrimPrefix(strings.TrimSpace(r), "│"), "│"))
	}
	if strings.Contains(strings.Join(expanded, ""), "…") {
		t.Errorf("expanded content should wrap, not ellipsise:\n%s", strings.Join(expanded, "\n"))
	}
	if !strings.Contains(strings.ReplaceAll(joined, " ", ""), strings.ReplaceAll(long, " ", "")[:40]) {
		t.Errorf("expanded content lost text while wrapping:\n%s", strings.Join(expanded, "\n"))
	}
}

// A folded multi-line value names the fold on its LABEL — which end, how much
// of how many — and the total moves as the value grows. No row is spent on it.
func TestRenderToolNode_FoldNoteRidesOnTheLabel(t *testing.T) {
	body := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d. a line\\n", i)
		}
		return b.String()
	}
	streaming := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"content":"` + body(9)}
	rows := renderNodeRows(t, streaming, 50, 10, false)
	if !strings.Contains(strings.Join(rows, "\n"), "content (…last "+fmt.Sprint(argStreamLines)+" of 9 lines)") {
		t.Errorf("streaming label should name the tail fold:\n%s", strings.Join(rows, "\n"))
	}
	// The total tracks the value: nine lines becomes twelve.
	streaming.Input = `{"content":"` + body(12)
	if rows = renderNodeRows(t, streaming, 50, 10, false); !strings.Contains(strings.Join(rows, "\n"), "of 12 lines") {
		t.Errorf("fold note should follow the value as it grows:\n%s", strings.Join(rows, "\n"))
	}
	settled := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args: map[string]any{"content": strings.ReplaceAll(body(9), "\\n", "\n")}}
	rows = renderNodeRows(t, settled, 50, 10, false)
	if !strings.Contains(strings.Join(rows, "\n"), "(…first "+fmt.Sprint(argSettledLines)+" of 9 lines)") {
		t.Errorf("settled label should name the HEAD fold:\n%s", strings.Join(rows, "\n"))
	}
}

// THE INVARIANT the owner asked for: the duration is on screen at every width.
// It was appended after an 80-column summary before, and the summary shoved it
// off the right at 65% of his tool calls.
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

// A settled tool keeps its arguments, folded to the FIRST argSettledLines rows
// of each value: nothing is moving any more, so the useful part is the head —
// what this call is — not the tail, which is only where it stopped.
func TestRenderToolNode_SettledArgsPreviewFromTheHead(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Summary: "git push", Args: map[string]any{"command": "git push origin main", "timeout": 240},
		Output: "everything up-to-date",
	}
	joined := strings.Join(renderNodeRows(t, n, 60, nodeBashCapDefault, false), "\n")
	for _, want := range []string{"command git push origin main", "timeout 240"} {
		if !strings.Contains(joined, want) {
			t.Errorf("settled box should carry %q:\n%s", want, joined)
		}
	}
}

// Ctrl-O adds METADATA and nothing else. Verbosity and "open this one thing"
// are different questions; one key answering both meant neither could be asked
// alone.
func TestRenderToolNode_VerboseAddsMetadataNotContent(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args:      map[string]any{"content": strings.Repeat("a line\n", 20)},
		StartedAt: 1785862036094, FinishedAt: 1785862036099,
	}
	folded := renderToolNode(n, 60, nodeBashCapDefault, 0, false, false)
	verbose := renderToolNode(n, 60, nodeBashCapDefault, 0, true, false)
	extra := len(verbose) - len(folded)
	if extra != 2 {
		t.Fatalf("Ctrl-O added %d rows, want exactly the two timestamps", extra)
	}
	joined := strings.Join(verbose, "\n")
	if !strings.Contains(stripANSI(joined), "  │ started ") {
		t.Errorf("timestamps must sit INSIDE the block:\n%s", stripANSI(joined))
	}
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
		t.Error("a settled tool folds its arguments, so it has something to reveal")
	}
	if nodeExpandable(livedoc.Node{Type: livedoc.NodeTool, Name: "bash"}) {
		t.Error("a tool with neither arguments nor output reveals nothing")
	}
}

// Arguments and the tool NAME are drawn in one colour (Kanagawa springBlue),
// and the block's rule is drawn in the SAME dim the output rule uses. Vacuous
// without colour, load-bearing with it — and it is the thing the owner asked
// for twice: one blue for the call, furniture identical on both sides.
func TestRenderToolNode_ColoursAreConsistent(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/x.md"}`}
	joined := strings.Join(renderToolNode(n, 60, 10, 0, false, false), "\n")
	for _, want := range []string{term.Arg("write"), term.Arg("/x.md"), term.Dim(quoteGutter)} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%q", want, joined)
		}
	}
}

// boxContentText returns the text inside a box row, or "" for an edge, an air
// row or a row that is not part of a box.
func boxContentText(plain string) string {
	i := strings.Index(plain, "│")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(plain[i+len("│"):])
}

func assertRows(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n got %q\nwant %q", i, got[i], want[i])
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
	// Count the box's CONTENT rows — interior rows carrying text — rather than
	// every row with a border in it, so the assertions survive a change to the
	// frame and stay about what is shown.
	ruleRows := func(view ldrender.NodeView, expanded func(int) bool, node livedoc.Node) int {
		c := ldrender.Composer{View: view, Tick: 0, Expanded: expanded}
		count := 0
		for _, r := range c.Nodes([]livedoc.Node{node}, 70) {
			if boxContentText(stripANSI(r.Text)) != "" {
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
	// The label, the moving window, and the `path` field's own row.
	wantArgs := 2 + argStreamLines
	args, out := rowsOf(&ariaView{settings: &renderSettings{}}, nil)
	if args != wantArgs {
		t.Errorf("incipit arguments: %d rows, want %d (the moving window)", args, wantArgs)
	}
	if out != 40 {
		t.Errorf("incipit output: %d rows, want all 40 — inline never collapses what it cannot reopen", out)
	}

	// The pager always states the per-node answer, and an unexpanded node
	// gets the same window — one shape, two surfaces.
	unexpanded := func(int) bool { return false }
	pagerArgs, pagerOut := rowsOf(pagerView(&ariaView{settings: &renderSettings{}}), unexpanded)
	if pagerArgs != wantArgs {
		t.Errorf("pager unexpanded arguments: %d rows, want %d", pagerArgs, wantArgs)
	}
	if pagerOut != nodeBashCapDefault+1 { // + the "… last N of M lines" note
		t.Errorf("pager unexpanded output: %d rows, want %d — the pager DOES collapse output", pagerOut, nodeBashCapDefault+1)
	}
}

// Two clocks: the header carries GENERATION (how long the model spent writing
// the call), the junction carries RUNTIME. They differ by thirty seconds on a
// large write, and only the second used to exist — which is why such a write
// rendered `[0ms]` after half a minute of streaming.
func TestRenderToolNode_TwoClocks(t *testing.T) {
	opened := int64(1785862030000)
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args: map[string]any{"path": "/x.md"}, Output: "Wrote 5 bytes",
		OpenedAt: opened, StartedAt: opened + 31200, FinishedAt: opened + 31204,
	}
	rows := renderNodeRows(t, n, 60, 10, false)
	if !strings.Contains(rows[0], "[31.2s]") {
		t.Errorf("header should carry the generation clock: %q", rows[0])
	}
	var junction string
	for _, r := range rows {
		if strings.Contains(r, "done") {
			junction = r
		}
	}
	if !strings.Contains(junction, "[4ms]") {
		t.Errorf("junction should carry the runtime: %q", junction)
	}

	// An aria older than the opened_at clock has no generation to show, so the
	// header falls back to the runtime rather than to a blank.
	old := n
	old.OpenedAt = 0
	if got := renderNodeRows(t, old, 60, 10, false)[0]; !strings.Contains(got, "[4ms]") {
		t.Errorf("without opened_at the header should fall back to the runtime: %q", got)
	}
}
