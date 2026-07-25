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
		{Turn: aria.Turn{ID: 1, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Role: "user", Markdown: "please look"}}}},
		{Turn: aria.Turn{ID: uint64(2), Sealed: true, Nodes: nodes}},
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
	var b strings.Builder
	section := func(name string, rows []string) {
		b.WriteString("## " + name + "\n")
		for _, r := range rows {
			b.WriteString(strconv.Quote(r) + "\n")
		}
	}

	tr := frameFixture(t)
	section("plain", tr.lines())

	tr.selectNode(1, false) // first node of the first message
	tr.selectNode(1, true)  // extend across the message boundary
	tr.selectNode(1, true)  //
	section("selected", tr.lines())

	tr.clearSelection()
	tr.matchQuery = "fox"
	section("highlighted", tr.lines())

	tr.matchQuery = ""
	tr.toggleSelectedTools() // no selection: inert
	tr.selection = nodeSelection{}
	section("plain-again", tr.lines())

	got := b.String()
	// Selection decoration branches on term.Enabled(), so both branches get a
	// golden; run the package a second time with FORCE_COLOR=1 to cover the
	// styled one.
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
