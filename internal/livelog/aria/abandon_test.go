package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// TestAbandon_DropsOpenWithoutCommitting: a partial open unit that is abandoned
// must NOT become a committed message (no duplication next turn) and must not
// broadcast a close marker; a subsequent Read must not resurrect it.
func TestAbandon_DropsOpenWithoutCommitting(t *testing.T) {
	s := NewServer()
	var frames []AriaRead
	s.Subscribe(func(r AriaRead) { frames = append(frames, r) })

	s.Open(1, "assistant")
	s.Update([]livedoc.Node{{Type: "text", Markdown: "partial thinking"}})

	committedFramesBefore := 0
	for _, f := range frames {
		if len(f.Committed) > 0 {
			committedFramesBefore++
		}
	}

	s.Abandon()

	// No new committed frame from Abandon.
	committedFramesAfter := 0
	for _, f := range frames {
		if len(f.Committed) > 0 {
			committedFramesAfter++
		}
	}
	if committedFramesAfter != committedFramesBefore {
		t.Fatalf("Abandon broadcast a committed frame (%d -> %d)", committedFramesBefore, committedFramesAfter)
	}

	// Read must expose neither a closed message nor a live one.
	r := s.Read(0)
	if len(r.Committed) != 0 {
		t.Fatalf("abandoned unit leaked into Read as committed: %+v", r.Committed)
	}
	if r.Live != nil {
		t.Fatalf("abandoned unit leaked into Read as live: %+v", r.Live)
	}

	// A fresh unit at a new LT still commits normally.
	s.Open(2, "assistant")
	s.Update([]livedoc.Node{{Type: "text", Markdown: "real answer"}})
	s.Close()
	r = s.Read(0)
	if len(r.Committed) != 1 || r.Committed[0].LT != 2 {
		t.Fatalf("after abandon, expected only LT2 committed, got %+v", r.Committed)
	}
}
