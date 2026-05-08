package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/rpc"
)

// safeBuf is a goroutine-safe wrapper around bytes.Buffer for the
// batch tests. The spinner goroutine writes concurrently with test
// goroutine reads; without locking, the race detector (rightly)
// complains.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func entries() []rpc.ToolBatchToolEntry {
	return []rpc.ToolBatchToolEntry{
		{ToolCallID: "a", ToolName: "bash", Arguments: map[string]interface{}{"command": "pwd"}},
		{ToolCallID: "b", ToolName: "bash", Arguments: map[string]interface{}{"command": "ls"}},
		{ToolCallID: "c", ToolName: "bash", Arguments: map[string]interface{}{"command": "uname -a"}},
	}
}

// stripANSI removes CSI escape sequences so test assertions can match
// the visible content without worrying about colors.
func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if (c >= 0x40 && c <= 0x7e) {
					j++
					break
				}
				j++
			}
			i = j - 1
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func TestBatchInitialRender(t *testing.T) {
	var buf safeBuf
	b := newToolBatchState(&buf, entries())
	b.RenderInitial()
	defer b.Finalize()

	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "batch (3)") {
		t.Fatalf("expected opening rule, got:\n%s", visible)
	}
	// Pending rows show a spinner glyph (any of spinnerFrames) plus
	// the tool detail. We only assert detail strings — the spinner
	// frame is animated and may have advanced.
	for _, want := range []string{"bash · pwd", "bash · ls", "bash · uname -a"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("missing pending row %q in:\n%s", want, visible)
		}
	}
}

func TestBatchMarkDoneRewritesRow(t *testing.T) {
	var buf safeBuf
	b := newToolBatchState(&buf, entries())
	b.RenderInitial()
	b.MarkRunning("a")
	b.MarkRunning("b")
	b.MarkRunning("c")

	b.MarkDone("a", "ok", false)
	b.MarkDone("b", "boom", true)
	b.Finalize()

	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "✓ bash · pwd") {
		t.Fatalf("expected updated success row in output:\n%s", visible)
	}
	if !strings.Contains(visible, "✗ bash · ls") {
		t.Fatalf("expected updated error row in output:\n%s", visible)
	}
	// 'c' was never marked done; should NOT appear with ✓ or ✗.
	if strings.Contains(visible, "✓ bash · uname -a") || strings.Contains(visible, "✗ bash · uname -a") {
		t.Fatalf("row c should remain non-finalized, got:\n%s", visible)
	}
}

func TestBatchFinalizeDumpsErrorOutput(t *testing.T) {
	var buf safeBuf
	b := newToolBatchState(&buf, entries())
	b.RenderInitial()
	b.MarkRunning("a")
	b.AppendOutput("a", "stack trace line 1\nstack trace line 2\n")
	b.MarkDone("a", "command failed", true)
	b.MarkDone("b", "fine", false)
	b.MarkDone("c", "fine", false)
	b.Finalize()

	visible := stripANSI(buf.String())
	if !strings.Contains(visible, "stack trace line 1") {
		t.Fatalf("expected error tool's buffered output to be dumped on Finalize:\n%s", visible)
	}
}

func TestBatchAppendOutputCap(t *testing.T) {
	var buf safeBuf
	b := newToolBatchState(&buf, entries())
	b.RenderInitial()
	defer b.Finalize()
	// Write more than 64 KiB; assert capped.
	huge := strings.Repeat("x", 70*1024)
	b.AppendOutput("a", huge)
	got := b.rows[0].output.Len()
	if got > 64*1024 {
		t.Fatalf("AppendOutput exceeded cap: %d bytes", got)
	}
}

func TestFormatRowElapsedShape(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "<1ms"},
		{42, "42ms"},
		{1500, "1.5s"},
	}
	for _, tc := range cases {
		d := timeMS(tc.ms)
		got := formatRowElapsed(d)
		if got != tc.want {
			t.Fatalf("formatRowElapsed(%dms) = %q want %q", tc.ms, got, tc.want)
		}
	}
}

// timeMS is a tiny helper to keep the table compact.
func timeMS(ms int64) (d timeDuration) {
	return timeDuration(ms) * 1_000_000
}

type timeDuration = time.Duration
