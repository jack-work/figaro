package tool_test

import (
	"testing"

	"github.com/jack-work/figaro/internal/tool"
)

func TestSummarize_BashCommand(t *testing.T) {
	b := &tool.BashTool{}
	if got := b.Summarize(map[string]any{"command": "ls -la"}); got != "ls -la" {
		t.Errorf("bash summarize: got %q, want %q", got, "ls -la")
	}
	if got := b.Summarize(map[string]any{}); got != "" {
		t.Errorf("bash summarize empty args: got %q", got)
	}
}

func TestSummarize_FilePath(t *testing.T) {
	w := tool.NewWriteTool("")
	if got := w.Summarize(map[string]any{"path": "/tmp/x"}); got != "/tmp/x" {
		t.Errorf("write summarize: got %q", got)
	}
	r := tool.NewReadTool("")
	if got := r.Summarize(map[string]any{"path": "/tmp/y"}); got != "/tmp/y" {
		t.Errorf("read summarize: got %q", got)
	}
	e := tool.NewEditTool("")
	if got := e.Summarize(map[string]any{"path": "/tmp/z"}); got != "/tmp/z" {
		t.Errorf("edit summarize: got %q", got)
	}
}

func TestSummarizer_NilSafe(t *testing.T) {
	fn := tool.Summarizer(nil)
	if fn == nil {
		t.Fatal("Summarizer(nil) must return a non-nil fn")
	}
	if got := fn("bash", map[string]any{"command": "x"}); got != "" {
		t.Errorf("nil-registry summarizer must return \"\", got %q", got)
	}
}

func TestSummarizer_Registry(t *testing.T) {
	r := tool.NewRegistry()
	r.MustRegister(&tool.BashTool{}, tool.NewWriteTool(""))
	fn := tool.Summarizer(r)
	if got := fn("bash", map[string]any{"command": "echo hi"}); got != "echo hi" {
		t.Errorf("bash via registry: got %q", got)
	}
	if got := fn("write", map[string]any{"path": "/p"}); got != "/p" {
		t.Errorf("write via registry: got %q", got)
	}
	// Unknown tool falls back to "".
	if got := fn("unknown", map[string]any{"x": 1}); got != "" {
		t.Errorf("unknown tool: got %q", got)
	}
}
