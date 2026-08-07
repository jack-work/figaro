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
	"github.com/jack-work/figaro/internal/tool"
)

var updateToolBlocks = flag.Bool("update-tool-blocks", false, "rewrite testdata/tool_blocks.golden")

// The tool block's SHAPE lives in one golden rather than in a dozen imperative
// assertions. Every round of layout feedback used to mean rewriting each
// expectation by hand; now it means reading a diff and running:
//
//	go test ./internal/cli -run ToolBlockGolden -update-tool-blocks
//
// A golden is the right instrument here because the property under test is
// "does this look right", which a human reads and a predicate does not. What
// stays imperative is what a snapshot cannot say: the width INVARIANT (every
// width from 20 to 200), the fold BEHAVIOUR under a gesture, and the colour
// contract — those are in nodes_test.go and transcript_expand_test.go.
//
// The cases are the states the owner reviewed, in the order he reviewed them.
// The cases are the states the owner reviewed, across the tool styles the
// table knows: a shell, a file write, a read, and one it has never heard of.
func toolBlockCases() []struct {
	name              string
	node              livedoc.Node
	width, bashCap    int
	verbose, expanded bool
} {
	o := int64(1785862030000)
	cmd := `cd /var/tmp/x && grep -niE "bass|baritone|tenor|soprano" opera.md`
	out := strings.TrimRight(strings.Repeat("15:13. Figaro, the barber of the title, is a baritone.\n", 13), "\n")
	body := strings.TrimRight(strings.Repeat("33. Rossini's Almaviva is a demanding tenor part.\n", 13), "\n")

	bash := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": cmd, "timeout": 240}, Output: out,
		OpenedAt: o, StartedAt: o + 1400, FinishedAt: o + 1407}
	write := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args:     map[string]any{"path": "/var/tmp/x/opera.md", "content": body},
		Output:   "/var/tmp/x/opera.md",
		OpenedAt: o, StartedAt: o + 31200, FinishedAt: o + 31204}
	streaming := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input: `{"path":"/var/tmp/x/opera.md","content":"` + strings.ReplaceAll(body, "\n", `\n`), OpenedAt: o}
	read := livedoc.Node{Type: livedoc.NodeTool, Name: "read", Status: livedoc.StatusOK,
		Args: map[string]any{"path": "/var/tmp/x/opera.md", "limit": 3}, Output: body,
		OpenedAt: o, StartedAt: o + 40, FinishedAt: o + 44}
	proc := livedoc.Node{Type: livedoc.NodeTool, Name: "process", Status: livedoc.StatusOK,
		Args: map[string]any{"action": "poll", "session": "bg-3"}, Output: "still running",
		OpenedAt: o, StartedAt: o + 5, FinishedAt: o + 9}
	unknown := livedoc.Node{Type: livedoc.NodeTool, Name: "mystery", Status: livedoc.StatusOK,
		Args: map[string]any{"zeta": "last", "alpha": "first"}, Output: "done",
		OpenedAt: o, StartedAt: o + 2, FinishedAt: o + 3}
	failed := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusError,
		Args: map[string]any{"command": "false"}, Output: "exit status 1",
		OpenedAt: o, StartedAt: o + 900, FinishedAt: o + 903}
	legacy := livedoc.Node{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": "ls"}, Output: "a\nb", StartedAt: o, FinishedAt: o + 4}

	oldText := "def retry(fn, retries=3):\n    for i in range(retries):\n        try:\n            return fn()\n        except Exception:\n            pass"
	newText := "def retry(fn, retries=DEFAULT_RETRIES):\n    last = None\n    for i in range(retries):\n        try:\n            return fn()\n        except Exception as e:\n            last = e\n    raise last"
	diff := tool.GenerateDiff(oldText+"\n", newText+"\n", 2)
	edit := livedoc.Node{Type: livedoc.NodeTool, Name: "edit", Status: livedoc.StatusOK,
		Args:     map[string]any{"path": "/var/tmp/x/retry.py", "old_text": oldText, "new_text": newText},
		Output:   diff.Diff,
		OpenedAt: o, StartedAt: o + 7300, FinishedAt: o + 7312}
	editing := livedoc.Node{Type: livedoc.NodeTool, Name: "edit", Status: livedoc.StatusRunning,
		Input:    `{"path":"/var/tmp/x/retry.py","old_text":"` + strings.ReplaceAll(oldText, "\n", `\n`) + `","new_text":"def retry(fn, retries=DEFAULT_RETRIES):\n    last = None\n    for i in ra`,
		OpenedAt: o}

	return []struct {
		name              string
		node              livedoc.Node
		width, bashCap    int
		verbose, expanded bool
	}{
		{"bash · minimized", bash, 78, nodeBashCapDefault, false, false},
		{"bash · expanded", bash, 78, nodeBashCapDefault, false, true},
		{"write · minimized (content is the body; no byte count)", write, 78, nodeBashCapDefault, false, false},
		{"write · expanded", write, 78, nodeBashCapDefault, false, true},
		{"write · streaming", streaming, 78, nodeBashCapDefault, false, false},
		{"read · minimized", read, 78, nodeBashCapDefault, false, false},
		{"process · drawn like a shell", proc, 78, nodeBashCapDefault, false, false},
		{"unknown tool · name and first argument", unknown, 78, nodeBashCapDefault, false, false},
		{"bash · failed", failed, 78, nodeBashCapDefault, false, false},
		{"bash · no generation clock (old aria)", legacy, 78, nodeBashCapDefault, false, false},
		{"bash · narrow pane", bash, 46, nodeBashCapDefault, false, false},
		{"edit · minimized (the diff is the body)", edit, 78, nodeBashCapDefault, false, false},
		{"edit · expanded (the result stands in; arguments only allude)", edit, 78, 6, false, true},
		{"edit · streaming (no result yet, so arguments are blocks)", editing, 78, 6, false, true},
	}
}

func TestToolBlockGolden(t *testing.T) {
	// A running call's duration is `now - opened`, so the clock is frozen at a
	// fixed point past the fixture's timestamps. Without this the snapshot
	// changes every second and the golden is worthless.
	// A running call's elapsed time is `now - opened`, so the clock is frozen
	// past the fixtures' timestamps. Without this the snapshot changes every
	// second and the golden is worthless.
	defer func(prev func() time.Time) { timeNow = prev }(timeNow)
	timeNow = func() time.Time { return time.UnixMilli(1785862030000 + 17_200) }

	var b strings.Builder
	for _, c := range toolBlockCases() {
		b.WriteString("## " + c.name + "\n")
		for _, r := range renderToolNode(c.node, c.width, c.bashCap, 0, c.verbose, c.expanded) {
			b.WriteString(strconv.Quote(stripANSI(r)) + "\n")
		}
	}
	got := b.String()

	path := filepath.Join("testdata", "tool_blocks.golden")
	if *updateToolBlocks {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run: go test ./internal/cli -run ToolBlockGolden -update-tool-blocks)", err)
	}
	if got == string(want) {
		return
	}
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
			t.Fatalf("tool block golden differs at line %d:\n got: %s\nwant: %s", i+1, g, w)
		}
	}
}
