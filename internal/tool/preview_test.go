package tool_test

import (
	"testing"

	"github.com/jack-work/figaro/internal/tool"
)

func TestPreviewArg_Write(t *testing.T) {
	w := tool.NewWriteTool("")
	if got := w.PreviewArg(); got != "content" {
		t.Errorf("write PreviewArg: got %q, want %q", got, "content")
	}
}

func TestPreviewArger_NilSafe(t *testing.T) {
	fn := tool.PreviewArger(nil)
	if fn == nil {
		t.Fatal("PreviewArger(nil) must return a non-nil fn")
	}
	if got := fn("write"); got != "" {
		t.Errorf("nil-registry PreviewArger must return \"\", got %q", got)
	}
}

func TestPreviewArger_Registry(t *testing.T) {
	r := tool.NewRegistry()
	r.MustRegister(&tool.BashTool{}, tool.NewWriteTool(""), tool.NewReadTool(""))
	fn := tool.PreviewArger(r)
	if got := fn("write"); got != "content" {
		t.Errorf("write via registry: got %q, want %q", got, "content")
	}
	// Tools that don't implement PreviewArg return "".
	if got := fn("bash"); got != "" {
		t.Errorf("bash (no PreviewArg): got %q", got)
	}
	if got := fn("read"); got != "" {
		t.Errorf("read (no PreviewArg): got %q", got)
	}
	// Unknown tool: "".
	if got := fn("nope"); got != "" {
		t.Errorf("unknown tool: got %q", got)
	}
}
