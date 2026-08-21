package aria

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A streamed tool argument is the largest string the delta path has ever been
// asked to carry: a 5 KB write body arrives as ~800 fragments and, at the
// 90ms emit interval, is re-diffed on every frame. The cost that matters is
// per FRAME, and it must stay a splice rather than a whole-value set.
//
// Sized from a measured turn: 4.8 KB of argument JSON, ~7 bytes per fragment.
func benchInput(frames int) []string {
	var b strings.Builder
	b.WriteString(`{"path":"/var/tmp/out.md","content":"`)
	out := make([]string, 0, frames)
	for i := range frames {
		fmt.Fprintf(&b, "%d. a line of the file that the model is writing\\n", i)
		out = append(out, b.String())
	}
	return out
}

func BenchmarkStreamedInputDelta(b *testing.B) {
	frames := benchInput(100)
	b.ReportAllocs()
	for b.Loop() {
		prev := livedoc.Node{Type: livedoc.NodeTool, Name: "write", Status: livedoc.StatusRunning}
		for _, f := range frames {
			next := prev
			next.Input = f
			_ = delta(0, prev, next)
			prev = next
		}
	}
}

// The same traffic end to end: compose's output through the server's diff
// into a client fold: which is what the CLI actually pays.
func BenchmarkStreamedInputServerToClient(b *testing.B) {
	frames := benchInput(100)
	b.ReportAllocs()
	for b.Loop() {
		srv := NewServer()
		cli := NewClient()
		srv.Subscribe(func(p Page) { cli.Apply(p) })
		srv.OpenTurn(1)
		for _, f := range frames {
			srv.Update(nil, []livedoc.Node{{
				Type: livedoc.NodeTool, ID: "t1", ToolCallID: "t1",
				Name: "write", Status: livedoc.StatusRunning, Input: f,
			}}, 0)
		}
	}
}

// The point of streaming the field rather than setting it: the bytes pushed
// over the socket must stay proportional to what the model TYPED, not to the
// argument size times the frame count. Without the splice, 100 frames of a
// growing 4.7 KB argument cost ~240 KB; with it, one small patch per frame.
//
// This is a perf assertion, not a benchmark, because it can fail.
func TestStreamedInputWireBytesStayProportional(t *testing.T) {
	frames := benchInput(100)
	final := frames[len(frames)-1]

	srv := NewServer()
	pushed := 0
	srv.Subscribe(func(p Page) {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		pushed += len(b)
	})
	srv.OpenTurn(1)
	for _, f := range frames {
		srv.Update(nil, []livedoc.Node{{
			Type: livedoc.NodeTool, ID: "t1", ToolCallID: "t1",
			Name: "write", Status: livedoc.StatusRunning, Input: f,
		}}, 0)
	}

	// Every byte typed crosses once, plus per-frame envelope. Whole-value sets
	// would cost sum(len(frame)) ~ 240 KB here; the budget is deliberately
	// loose enough not to be a tripwire on envelope changes and tight enough
	// that losing the splice fails it by an order of magnitude.
	budget := len(final) * 4
	if pushed > budget {
		t.Fatalf("pushed %d bytes for a %d-byte argument over %d frames (budget %d): is `input` still spliced?",
			pushed, len(final), len(frames), budget)
	}
	t.Logf("pushed %d bytes for a %d-byte argument over %d frames (%.1f× the typed bytes)",
		pushed, len(final), len(frames), float64(pushed)/float64(len(final)))
}
