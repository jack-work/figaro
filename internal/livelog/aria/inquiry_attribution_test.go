package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// WHO ASKED SURVIVES THE SECOND FRAME.
//
// OpenInquiry broadcasts the question WITH its segments. Every frame after it
// re-states the question, because a part must never be partial about the turn
// it describes — and the client HOLDS what a part last said. So a re-statement
// that carries the text without the segments is not a harmless omission: it
// overwrites an attributed inquiry with an unattributed one, and the live
// surfaces render a question nobody asked while `figaro show`, which re-derives
// segments from the IR, renders it correctly. Exactly one frame is enough to
// lose it, which is why this asserts on the frames and not on the seal.
func TestEveryFrameKeepsWhoAskedTheQuestion(t *testing.T) {
	s := NewServer()
	var pages []Page
	s.Subscribe(func(p Page) { pages = append(pages, p) })

	segs := []InquirySegment{{Sender: "aria 123456", Text: "who are you"}}
	s.OpenTurn(1)
	s.OpenInquiry(1, "who are you", segs...)
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "I am"}})
	s.Close()

	frames := 0
	for _, p := range pages {
		for _, part := range p.Parts {
			if part.Inquiry == "" {
				continue // a part that states no question states no askers
			}
			frames++
			if len(part.InquirySegments) != len(segs) {
				t.Fatalf("frame %d carried the question %q with %d segments, want %d — "+
					"the client holds what a part last said, so this erases the sender",
					frames, part.Inquiry, len(part.InquirySegments), len(segs))
			}
			if got := part.InquirySegments[0].Sender; got != segs[0].Sender {
				t.Fatalf("frame %d sender = %q, want %q", frames, got, segs[0].Sender)
			}
		}
	}
	if frames < 3 {
		t.Fatalf("only %d frames restated the question; open, update and close must all carry it", frames)
	}
}
