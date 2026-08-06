package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
)

var updateGolden = flag.Bool("update-frames", false, "rewrite testdata/transcript_frames.golden")

// frameFixture is a transcript over content chosen to exercise the row
// primitives: markdown prose that wraps, a tool node with captured output
// (dim gutter escapes), wide CJK runes, an over-width line that must be
// clipped, and embedded control characters that must be flattened.
func frameFixture(t *testing.T) *transcript {
	t.Helper()
	nodes := []livedoc.Node{
		{Type: livedoc.NodeThinking, Markdown: "weighing the options\nand a second line"},
		{Type: livedoc.NodeProse, Markdown: "The quick brown fox jumps over the lazy dog, repeatedly and at length, past the right margin.\n\n日本語のテキストもここにあります。"},
		{
			Type: livedoc.NodeTool, ID: "t1", Name: "bash",
			Args:    map[string]any{"command": "ls -la\nsecond line of command"},
			Status:  livedoc.StatusOK,
			Summary: "ls -la /a/very/long/path/that/will/be/truncated/in/the/header/line",
			Output:  "alpha\nbeta\ttabbed\ngamma 日本語\n" + strings.Repeat("wide ", 30),
		},
		{Type: livedoc.NodeProse, Markdown: "done."},
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: "please look", Sealed: true}},
		{Turn: aria.Turn{ID: uint64(2), Inquiry: "and again", Sealed: true, Nodes: nodes}},
	}})
	ft := ldrender.NewFakeTerminal(48, 14)
	tr := newTranscript(ft, 48, 14, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	return tr
}

// TestTranscriptFramesGolden pins the exact bytes of the materialized rows in
// three states: plain, with a node selection (gutter + background wash), and
// with a search highlight. This is the end-to-end guard for the allocation
// surgery in clipToWidth / decorateNodeRow — the frames must not move.
func TestTranscriptFramesGolden(t *testing.T) {
	got := buildGoldenFrames(t)

	// The pre-collapse companion is regenerated in the same pass, so the
	// cell-level proof beside these frames survives a content change instead
	// of silently becoming a comparison of two different fixtures.
	if *updateGolden {
		prev := sgrCollapse
		sgrCollapse = func(s string) string { return s }
		pre := buildGoldenFrames(t)
		sgrCollapse = prev
		name := "transcript_frames.pre-sgr.golden"
		if term.Enabled() {
			name = "transcript_frames_color.pre-sgr.golden"
		}
		if err := os.WriteFile(filepath.Join("testdata", name), []byte(pre), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeOrCompareGolden(t, got)
}

func buildGoldenFrames(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	section := func(name string, rows []string) {
		b.WriteString("## " + name + "\n")
		for _, r := range rows {
			b.WriteString(strconv.Quote(r) + "\n")
		}
	}

	tr := frameFixture(t)
	section("plain", tr.lines())

	// The first two selectable points are the two turns' INQUIRIES (a question
	// selects and copies exactly like a node — see inquiryNode); step past them
	// so this fixture keeps pinning the same three NODE rows it always did, and
	// the pre-sgr cell-level proof beside it stays a proof.
	//
	// The viewport is parked at the top first. ^N seeds from the VIEWPORT now —
	// the topmost block on screen — and frameFixture leaves the offset at the
	// bottom, so without this the walk starts further down and this golden would
	// pin different rows. Stating the precondition keeps the golden about row
	// BYTES, which is what it is for.
	tr.offset = 0
	tr.selectNode(1, false)
	tr.selectNode(1, false)
	tr.selectNode(1, false) // turn 2's first node
	tr.selectNode(1, true)  // extend across the message boundary
	tr.selectNode(1, true)  //
	section("selected", tr.lines())

	tr.clearSelection()
	tr.matchQuery = "fox"
	section("highlighted", tr.lines())

	tr.matchQuery = ""
	tr.toggleSelectedNodes() // no selection: inert
	tr.selection = nodeSelection{}
	section("plain-again", tr.lines())

	return b.String()
}

// writeOrCompareGolden writes the frames under -update-frames, else diffs them.
// Selection decoration branches on term.Enabled(), so both branches get a
// golden; run the package a second time with FORCE_COLOR=1 to cover the
// styled one.
func writeOrCompareGolden(t *testing.T, got string) {
	t.Helper()
	name := "transcript_frames.golden"
	if term.Enabled() {
		name = "transcript_frames_color.golden"
	}
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run: go test ./internal/cli/ -run Golden -update-frames)", err)
	}
	if got != string(want) {
		gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range max(len(gotLines), len(wantLines)) {
			g, w := "", ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Fatalf("frame golden mismatch at line %d:\n got: %s\nwant: %s", i+1, g, w)
			}
		}
		t.Fatal("frame golden mismatch")
	}
}
