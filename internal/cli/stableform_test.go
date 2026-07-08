package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

func joinRows(rows []string) string { return strings.Join(rows, "\n") }

// stableForm collapses a log-emitting tool to a one-line done-indication (no
// output body), while a non-log tool keeps its full output.
func TestStableForm_LogEmitCollapses(t *testing.T) {
	nodes := []livedoc.Node{
		{Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK,
			Args: map[string]interface{}{"command": "ls -la"}, Output: "SECRETLINE-a\nSECRETLINE-b\nSECRETLINE-c"},
		{Type: livedoc.NodeTool, Name: "read", Status: livedoc.StatusOK,
			Args: map[string]interface{}{"path": "/etc/hosts"}, Output: "KEEPLINE-1\nKEEPLINE-2"},
	}
	out := joinRows(stableForm(nodes, 0, len(nodes), 80, nodeBashCapDefault, renderSettings{}))

	if !strings.Contains(out, "bash") {
		t.Fatalf("bash done-indication missing:\n%s", out)
	}
	if strings.Contains(out, "SECRETLINE") {
		t.Fatalf("log-emit tool output leaked into stable form:\n%s", out)
	}
	// The command summary is fine on the header line; the streamed output is not.
	if !strings.Contains(out, "ls -la") {
		t.Fatalf("bash arg summary should remain on the header:\n%s", out)
	}
	// A non-log tool keeps its full output.
	if !strings.Contains(out, "KEEPLINE-1") {
		t.Fatalf("non-log tool output should be preserved:\n%s", out)
	}
}

// write is a log-emitting tool: its streamed preview never lands in scrollback.
func TestStableForm_WriteIsLogEmit(t *testing.T) {
	nodes := []livedoc.Node{
		{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusOK,
			Args: map[string]interface{}{"path": "/tmp/f"}, Output: "PREVIEW-line-1\nPREVIEW-line-2"},
	}
	out := joinRows(stableForm(nodes, 0, len(nodes), 80, nodeBashCapDefault, renderSettings{}))
	if strings.Contains(out, "PREVIEW-line") {
		t.Fatalf("write preview leaked into stable form:\n%s", out)
	}
	if !strings.Contains(out, "write") {
		t.Fatalf("write done-indication missing:\n%s", out)
	}
}

// A sub-range renders only the requested nodes, blank-separated, no outer blanks.
func TestStableForm_SubRange(t *testing.T) {
	nodes := []livedoc.Node{
		{Type: livedoc.NodeThinking, Markdown: "aaa"},
		{Type: livedoc.NodeProse, Markdown: "bbb"},
		{Type: livedoc.NodeThinking, Markdown: "ccc"},
	}
	out := stableForm(nodes, 1, 2, 80, nodeBashCapDefault, renderSettings{})
	joined := joinRows(out)
	if strings.Contains(joined, "aaa") || strings.Contains(joined, "ccc") {
		t.Fatalf("sub-range leaked neighbouring nodes:\n%s", joined)
	}
	if !strings.Contains(joined, "bbb") {
		t.Fatalf("sub-range missing its node:\n%s", joined)
	}
	if len(out) > 0 && out[0] == "" {
		t.Fatalf("stable form must not start with a blank row:\n%q", out)
	}
}
