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
func toolBlockCases() []struct {
	name              string
	node              livedoc.Node
	width, bashCap    int
	verbose, expanded bool
} {
	opened := int64(1785862030000)
	longCmd := "cd /var/tmp/x && grep -n -i 'rossini' opera.md && echo done with a tail far longer than the pane"
	body := "# Sixty Facts\n1. Il barbiere di Siviglia is an opera buffa in two acts by Rossini.\n2. The libretto was by Cesare Sterbini.\n3. It premiered in Rome in 1816.\n4. Rossini was twenty-three.\n5. He wrote it in under three weeks."
	out := strings.TrimRight(strings.Repeat("9:7. Mozart's Le nozze di Figaro sets the sequel.\n", 13), "\n")

	bash := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": longCmd}, Output: out,
		OpenedAt: opened, StartedAt: opened + 1400, FinishedAt: opened + 1407,
	}
	write := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
		Args:     map[string]any{"path": "/var/tmp/x/opera.md", "content": body},
		Output:   "Wrote 5453 bytes to /var/tmp/x/opera.md",
		OpenedAt: opened, StartedAt: opened + 31200, FinishedAt: opened + 31204,
	}
	streaming := livedoc.Node{
		Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning,
		Input:    `{"path":"/var/tmp/x/opera.md","content":"` + strings.ReplaceAll(body, "\n", `\n`),
		OpenedAt: opened,
	}
	failed := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusError,
		Args: map[string]any{"command": "false"}, Output: "exit status 1",
		OpenedAt: opened, StartedAt: opened + 900, FinishedAt: opened + 903,
	}
	running := livedoc.Node{
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning,
		Args: map[string]any{"command": "sleep 30"}, Output: "still going",
		OpenedAt: opened, StartedAt: opened + 200,
	}
	legacy := livedoc.Node{ // an aria older than the generation clock
		Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
		Args: map[string]any{"command": "ls"}, Output: "a\nb",
		StartedAt: opened, FinishedAt: opened + 4,
	}

	return []struct {
		name              string
		node              livedoc.Node
		width, bashCap    int
		verbose, expanded bool
	}{
		{"bash · folded", bash, 78, nodeBashCapDefault, false, false},
		{"bash · expanded (Enter)", bash, 78, nodeOutputUnlimited, false, true},
		{"bash · verbose (Ctrl-O) adds metadata only", bash, 78, nodeBashCapDefault, true, false},
		{"write · folded, head of a multiline value", write, 78, nodeBashCapDefault, false, false},
		{"write · expanded", write, 78, nodeOutputUnlimited, false, true},
		{"write · streaming, tail of a growing value", streaming, 78, nodeOutputUnlimited, false, false},
		{"write · streaming, expanded", streaming, 78, nodeOutputUnlimited, false, true},
		{"bash · failed", failed, 78, nodeBashCapDefault, false, false},
		{"bash · running, output arriving", running, 78, nodeBashCapDefault, false, false},
		{"bash · no generation clock (old aria)", legacy, 78, nodeBashCapDefault, false, false},
		{"bash · narrow pane", bash, 46, nodeBashCapDefault, false, false},
		{"bash · too narrow to frame", bash, 24, nodeBashCapDefault, false, false},
	}
}

func TestToolBlockGolden(t *testing.T) {
	// A running call's duration is `now - opened`, so the clock is frozen at a
	// fixed point past the fixture's timestamps. Without this the snapshot
	// changes every second and the golden is worthless.
	defer func(prev func() time.Time) { timeNow = prev }(timeNow)
	timeNow = func() time.Time { return time.UnixMilli(1785862030000 + 12_500) }

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
