package aria

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// EVERY PART CARRIES ITS TURN'S QUESTION.
//
// It used to ride exactly one frame — the OpenInquiry broadcast — and every
// streaming frame afterwards described the same turn without it. A client that
// had not folded that one frame before nodes arrived held a turn with content
// and no question, and nothing later re-supplied it: only the seal carries the
// whole Turn. That is why the question appeared when the turn ENDED.
//
// A part is a description of a turn, so it must state what the turn IS. The
// question is not a delta and cannot be reconstructed from one.
func TestEveryPartCarriesItsTurnsInquiry(t *testing.T) {
	const q = "WHATDIDIASK"
	var frames []Page
	s := NewServer()
	s.Subscribe(func(p Page) { frames = append(frames, p) })

	s.OpenInquiry(1, q)
	s.OpenTurn(1)
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering"}})
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering more"}})
	s.Close()

	if len(frames) < 3 {
		t.Fatalf("fixture broken: only %d frames", len(frames))
	}
	var missing []int
	for i, f := range frames {
		for _, part := range f.Parts {
			if part.ID == 1 && part.Inquiry != q {
				missing = append(missing, i)
			}
		}
	}
	if len(missing) > 0 {
		var b strings.Builder
		for _, i := range missing {
			b.WriteString("\n  frame ")
			b.WriteByte(byte('0' + i))
			b.WriteString(" describes turn 1 without its question")
		}
		t.Fatalf("%d of %d frames drop the inquiry:%s", len(missing), len(frames), b.String())
	}
}
