package aria

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	client "github.com/jack-work/figaro/sdk/aria"
)

// A streamed tool argument is re-diffed on every frame; the per-frame cost
// must stay a splice rather than a whole-value set.
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

func BenchmarkStreamedInputServerToClient(b *testing.B) {
	frames := benchInput(100)
	b.ReportAllocs()
	for b.Loop() {
		srv := NewServer()
		cli := client.NewClient()
		srv.Subscribe(func(p Page) { cli.Apply(p, client.Notify) })
		srv.OpenTurn(1)
		for _, f := range frames {
			srv.Update(nil, []livedoc.Node{{
				Type: livedoc.NodeTool, ID: "t1", ToolCallID: "t1",
				Name: "write", Status: livedoc.StatusRunning, Input: f,
			}}, 0)
		}
	}
}

// Bytes pushed must stay proportional to what the model typed, not to the
// argument size times the frame count. A perf assertion: it can fail.
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

	// The budget is loose enough to survive envelope changes and tight enough
	// that losing the splice fails it by an order of magnitude.
	budget := len(final) * 4
	if pushed > budget {
		t.Fatalf("pushed %d bytes for a %d-byte argument over %d frames (budget %d): is `input` still spliced?",
			pushed, len(final), len(frames), budget)
	}
	t.Logf("pushed %d bytes for a %d-byte argument over %d frames (%.1f× the typed bytes)",
		pushed, len(final), len(frames), float64(pushed)/float64(len(final)))
}
