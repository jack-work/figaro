package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

var updateGolden = flag.Bool("update-frames", false, "rewrite testdata/transcript_frames.golden")

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
	}}, aria.Notify)
	ft := ldrender.NewFakeTerminal(48, 14)
	tr := newTranscript(ft, 48, 14, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.follow = false
	return tr
}

func TestTranscriptFramesGolden(t *testing.T) {
	got := buildGoldenFrames(t)

	// The pre-collapse companion is regenerated in the same pass so the
	// cell-level proof beside it survives a content change.
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

	// The first two selectable points are the inquiries; park at the top first
	// because ^N seeds from the viewport.
	tr.offset = 0
	tr.selectNode(1, false)
	tr.selectNode(1, false)
	tr.selectNode(1, false)
	tr.selectNode(1, true)
	tr.selectNode(1, true)
	section("selected", tr.lines())

	tr.clearSelection()
	tr.matchQuery = "fox"
	section("highlighted", tr.lines())

	tr.matchQuery = ""
	tr.toggleSelectedNodes()
	tr.selection = nodeSelection{}
	section("plain-again", tr.lines())

	return b.String()
}

// Selection decoration branches on term.Enabled(), so both branches have a
// golden; run again with FORCE_COLOR=1 for the styled one.
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
