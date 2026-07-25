package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// ---------------------------------------------------------------------------
// Virtualization equivalence.
//
// render() no longer materializes the whole retained window; it indexes it and
// paints only the visible slice. legacyLines below is a verbatim copy of the
// pre-virtualization pass (whole-window materialization, selection decoration
// and search highlighting applied to every row, nodeRows accumulated as it
// goes). It is the oracle: every window the new code paints must be
// byte-identical to the corresponding slice of it, in every state.
// ---------------------------------------------------------------------------

func legacyLines(t *transcript) ([]string, []int, map[nodeRef]nodeSpan) {
	if t.follow {
		t.resetToTail()
	}
	if t.cacheW != t.w {
		t.rowCache = map[int]cachedMessage{}
		t.cacheW = t.w
	}
	marks := t.selectionMarks()
	hl := t.activeHighlight()
	var out []string
	var lts []int
	nodeRows := map[nodeRef]nodeSpan{}
	appendMsg := func(rows []transcriptRow, lt int) {
		if len(out) > 0 {
			out = append(out, "", dimTransRule(t.w), "")
			lts = append(lts, lt, lt, lt)
		}
		for _, r := range rows {
			line := r.text
			if r.ref.valid() {
				line = decorateNodeRow(line, marks[r.ref], t.w)
				span, ok := nodeRows[r.ref]
				if !ok {
					span.first = len(out)
				}
				span.last = len(out)
				nodeRows[r.ref] = span
			}
			if hl != "" {
				line = highlightMatches(line, hl)
			}
			out = append(out, line)
			lts = append(lts, lt)
		}
	}
	for _, m := range t.messages() {
		rows, ok := t.rowCache[m.LT]
		if !ok {
			rows = t.renderMsgBase(m)
			t.rowCache[m.LT] = rows
		}
		appendMsg(rows.rows, m.LT)
	}
	if open := t.openMessage(); open != nil {
		appendMsg(t.renderMsgBase(*open).rows, open.LT)
	}
	return out, lts, nodeRows
}

// mixedNodes exercises every row shape the pager can emit: header rows with no
// ref, thinking, wrapped prose, a tool with captured output, blank separators
// between nodes.
func mixedNodes(seed int) []livedoc.Node {
	var prose strings.Builder
	for p := range 2 {
		fmt.Fprintf(&prose, "Paragraph %d of message %d: needle and thread. ", p, seed)
		for range 3 {
			prose.WriteString("The quick brown fox jumps over the lazy dog past the right margin. ")
		}
		prose.WriteString("\n\n")
	}
	var out strings.Builder
	for i := range 12 {
		fmt.Fprintf(&out, "%4d captured needle output line for message %d\n", i, seed)
	}
	return []livedoc.Node{
		{Type: livedoc.NodeThinking, Markdown: fmt.Sprintf("Thinking about %d.\nSecond thought.", seed)},
		{Type: livedoc.NodeProse, Markdown: prose.String()},
		{
			Type: livedoc.NodeTool, ID: fmt.Sprintf("t%d", seed), Name: "bash",
			Args: map[string]any{"command": "rg needle"}, Status: livedoc.StatusOK,
			Summary: "rg needle", Output: out.String(),
		},
		{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("Tail prose %d.", seed)},
	}
}

func mixedTranscript(tb testing.TB, out io.Writer, w, h, n int) (*transcript, *aria.Client) {
	tb.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.Committed, n)
	for i := range committed {
		role := "assistant"
		if i%3 == 0 {
			role = "user"
		}
		committed[i] = aria.Committed{LT: i + 1, Role: role, Nodes: mixedNodes(i + 1)}
	}
	client.Apply(aria.AriaRead{Committed: committed})
	tr := newTranscript(out, w, h, &ariaView{settings: &renderSettings{}}, client, "virt1234", time.Unix(0, 0))
	tr.enter()
	return tr, client
}

// assertWindowMatchesLegacy sweeps every viewport position and asserts the
// virtualized window is byte-identical to the oracle's slice, that lines() and
// lineLT still agree with it, and that node spans survived the move off the
// materialization pass.
func assertWindowMatchesLegacy(t *testing.T, tr *transcript, body int) {
	t.Helper()
	want, wantLT, wantSpans := legacyLines(tr)
	tr.buildIndex()
	if tr.index.total != len(want) {
		t.Fatalf("index total = %d, want %d", tr.index.total, len(want))
	}
	if got := tr.lines(); !equalStrings(got, want) {
		t.Fatalf("lines() diverged from the legacy materialization at %s", firstDiff(got, want))
	}
	if !equalInts(tr.lineLT, wantLT) {
		t.Fatalf("lineLT diverged (len %d vs %d)", len(tr.lineLT), len(wantLT))
	}
	for ref, span := range wantSpans {
		got, ok := tr.nodeSpanOf(ref)
		if !ok || got != span {
			t.Fatalf("nodeSpanOf(%v) = %v,%v; want %v", ref, got, ok, span)
		}
	}
	var buf []string
	for off := 0; off+body <= len(want); off++ {
		buf = tr.window(off, off+body, buf)
		if !equalStrings(buf, want[off:off+body]) {
			t.Fatalf("window(%d,%d) diverged at %s", off, off+body, firstDiff(buf, want[off:off+body]))
		}
	}
	// Clamping at both ends.
	buf = tr.window(-5, 4, buf)
	if !equalStrings(buf, want[:4]) {
		t.Fatalf("window clamped low diverged")
	}
	buf = tr.window(len(want)-3, len(want)+9, buf)
	if !equalStrings(buf, want[len(want)-3:]) {
		t.Fatalf("window clamped high diverged")
	}
	if buf = tr.window(len(want), len(want)+5, buf); len(buf) != 0 {
		t.Fatalf("window past the end returned %d rows", len(buf))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstDiff(got, want []string) string {
	for i := range max(len(got), len(want)) {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			return fmt.Sprintf("row %d:\n got %q\nwant %q", i, g, w)
		}
	}
	return fmt.Sprintf("length %d vs %d", len(got), len(want))
}

func TestTranscriptVirtualWindow_PlainScroll(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 6)
	tr.scrollBy(-1)
	assertWindowMatchesLegacy(t, tr, 21)
}

func TestTranscriptVirtualWindow_SearchHighlight(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 6)
	tr.scrollBy(-1)
	tr.matchQuery = "needle"
	assertWindowMatchesLegacy(t, tr, 21)

	// Live typing highlights the in-progress query instead.
	tr.inSearch, tr.query = true, "fox"
	assertWindowMatchesLegacy(t, tr, 21)
}

func TestTranscriptVirtualWindow_NodeSelection(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 6)
	tr.scrollBy(-1)
	messages := tr.messages()
	tr.selection = nodeSelection{
		active: true,
		anchor: testSelectionPoint(messages[1].LT, 1, messages[1].Nodes[1]),
		focus:  testSelectionPoint(messages[3].LT, 0, messages[3].Nodes[0]),
	}
	assertWindowMatchesLegacy(t, tr, 21)

	// A single-node selection: the focused node also carries the bright gutter.
	tr.selection.anchor = tr.selection.focus
	assertWindowMatchesLegacy(t, tr, 21)

	// Selection plus an active highlight, both applied to the same rows.
	tr.matchQuery = "needle"
	tr.selection.anchor = testSelectionPoint(messages[1].LT, 1, messages[1].Nodes[1])
	assertWindowMatchesLegacy(t, tr, 21)
}

func TestTranscriptVirtualWindow_ExpandedTools(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 4)
	tr.scrollBy(-1)
	messages := tr.messages()
	ref := nodeRef{lt: messages[2].LT, index: 2}
	tr.expanded[ref] = true
	delete(tr.rowCache, ref.lt)
	assertWindowMatchesLegacy(t, tr, 21)
}

func TestTranscriptVirtualWindow_LiveTailFollow(t *testing.T) {
	tr, client := mixedTranscript(t, io.Discard, 80, 24, 4)
	client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: 5, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{ID: "live", Set: map[string]any{
			"type": string(livedoc.NodeProse), "markdown": "streaming needle prose",
		}}},
	}})
	tr.render()
	if !tr.follow {
		t.Fatal("expected the pager to be following the live tail")
	}
	assertWindowMatchesLegacy(t, tr, 21)

	// Growing the open message must move the index, not stale it.
	client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: 5, V: 1,
		Nodes: []aria.NodeDelta{
			{ID: "live", Set: map[string]any{"markdown": "streaming needle prose, now rather longer"}},
			{ID: "live2", Set: map[string]any{
				"type": string(livedoc.NodeThinking), "markdown": "a second live node\nwith two lines",
			}},
		},
	}})
	tr.render()
	assertWindowMatchesLegacy(t, tr, 21)
}

func TestTranscriptVirtualWindow_WidthChangeInvalidates(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 4)
	tr.scrollBy(-1)
	assertWindowMatchesLegacy(t, tr, 21)
	tr.resize(52, 24)
	assertWindowMatchesLegacy(t, tr, 21)
	tr.resize(120, 30)
	assertWindowMatchesLegacy(t, tr, 27)
}

// TestTranscriptVirtualFrames_ScrollIsPure pins the headline invariant: moving
// the viewport and coming back paints exactly the same frame, and each frame
// equals the legacy materialization at that offset.
func TestTranscriptVirtualFrames_ScrollIsPure(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 24)
	tr, _ := mixedTranscript(t, ft, 80, 24, 6)
	tr.matchQuery = "needle"
	tr.scrollBy(-1)

	frames := map[int][]string{}
	for range 8 {
		tr.key('k')
		frames[tr.offset] = append([]string(nil), ft.Screen()...)
	}
	for range 8 {
		tr.key('j')
		if want, ok := frames[tr.offset]; ok {
			if got := ft.Screen(); !equalStrings(got, want) {
				t.Fatalf("frame at offset %d changed on the way back: %s", tr.offset, firstDiff(got, want))
			}
		}
	}
	// gg / G land on the extremes and still agree with the oracle.
	tr.key('g')
	tr.key('g')
	if tr.offset != 0 {
		t.Fatalf("gg left offset %d", tr.offset)
	}
	assertWindowMatchesLegacy(t, tr, 21)
	tr.key('G')
	assertWindowMatchesLegacy(t, tr, 21)
}

// TestTranscriptVirtualFrames_ResizeAnchor pins that the anchoring path still
// sees a current lineLT after the index took over its maintenance.
func TestTranscriptVirtualFrames_ResizeAnchor(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 24)
	tr, _ := mixedTranscript(t, ft, 80, 24, 6)
	tr.scrollBy(-40)
	before := tr.lineLT[tr.offset]
	ft.Resize(60, 24)
	tr.resize(60, 24)
	if got := tr.lineLT[tr.offset]; got != before {
		t.Fatalf("resize moved the anchor from LT %d to %d", before, got)
	}
	assertWindowMatchesLegacy(t, tr, 21)
}

// TestTranscriptVirtualSelectionVisible pins that ensureSelectionVisible still
// scrolls to the focused node now that spans come from the index.
func TestTranscriptVirtualSelectionVisible(t *testing.T) {
	tr, _ := mixedTranscript(t, io.Discard, 80, 24, 6)
	tr.scrollBy(-1)
	for range 4 {
		tr.selectNode(-1, false)
	}
	span, ok := tr.nodeSpanOf(tr.selection.focus.nodeRef)
	if !ok {
		t.Fatal("focused node has no span")
	}
	body := tr.h - 1
	if span.first < tr.offset || span.last >= tr.offset+body {
		t.Fatalf("focused node span %v outside viewport [%d,%d)", span, tr.offset, tr.offset+body)
	}
}
