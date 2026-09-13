package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// What an adornment costs a frame. The paint path gained two decisions per
// row (a gutter glyph, a snake column) and the open state gained rows, so
// both are measured against the same window with no state at all.

func benchAdornDeltas(i int) map[string]livedoc.FormDelta {
	out := map[string]livedoc.FormDelta{}
	for k, key := range []string{"mantra", "datetime", "phase"} {
		out["a1."+key] = livedoc.FormDelta{
			Form: "a1", Kind: livedoc.FormBound, Event: livedoc.FormSet,
			Prev:  json.RawMessage(fmt.Sprintf(`"old value %d-%d for a key"`, i, k)),
			Value: json.RawMessage(fmt.Sprintf(`"new value %d-%d for a key, longer than the old one"`, i, k)),
		}
	}
	return out
}

// benchAdorned is a window of turns whose every block carries form state.
func benchAdorned(b *testing.B, turns int, deltas bool) *transcript {
	b.Helper()
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	parts := make([]aria.TurnPart, turns)
	for i := range parts {
		node := livedoc.Node{
			Type:     livedoc.NodeProse,
			Markdown: fmt.Sprintf("message %05d carries enough prose to wrap across a typical terminal row", i+1),
		}
		turn := aria.Turn{ID: uint64(i + 1), Sealed: true, Inquiry: fmt.Sprintf("question %d", i+1)}
		if deltas {
			node.FormDeltas = benchAdornDeltas(i)
			turn.FormDeltas = benchAdornDeltas(i)
		}
		turn.Nodes = []livedoc.Node{node}
		parts[i] = aria.TurnPart{Turn: turn}
	}
	client.Apply(aria.Page{Parts: parts}, aria.Notify)
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "benchmark", time.Unix(0, 0))
	tr.enter()
	return tr
}

func BenchmarkAdornmentFrame(b *testing.B) {
	for _, c := range []struct {
		name   string
		deltas bool
		open   bool
		cursor bool
	}{
		{name: "none"},
		{name: "collapsed", deltas: true},
		{name: "open", deltas: true, open: true},
		{name: "open+cursor", deltas: true, open: true, cursor: true},
	} {
		b.Run(c.name, func(b *testing.B) {
			tr := benchAdorned(b, 2_000, c.deltas)
			if c.open {
				tr.forEachMessage(func(m aria.Message) {
					tr.adorned[nodeRef{turn: m.Turn, index: inquiryNode}] = true
					for i := range m.Nodes {
						tr.adorned[nodeRefAt(m, i)] = true
					}
				})
				tr.rowCache = map[sliceKey]cachedMessage{}
				tr.buildIndex()
			}
			if c.cursor {
				tr.selectNode(-1, false)
				tr.selectNode(-1, false)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				tr.rowBuf = tr.window(tr.offset, tr.offset+38, tr.rowBuf)
			}
		})
	}
}

// BenchmarkAdornmentFold is the gesture: the rows it drops are recomposed by
// the frame after it, so the pair is what a keypress costs.
func BenchmarkAdornmentFold(b *testing.B) {
	tr := benchAdorned(b, 2_000, true)
	tr.selectNode(-1, false)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		tr.toggleSelectedNodes()
		tr.rowBuf = tr.window(tr.offset, tr.offset+38, tr.rowBuf)
	}
}
