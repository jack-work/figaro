package compose

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// A transcript draws an attributed question from its segments, not from the
// inquiry text, so the picture's line must ride the first segment too.
func TestAnAttributedQuestionStillNamesItsPicture(t *testing.T) {
	m := message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/png", "BYTES"),
		message.SenderText("Gluck", "total?"),
	}}
	segs := InquirySegmentsOf(m)
	if len(segs) != 1 || segs[0].Text != "[image: image/png]\n\ntotal?" || segs[0].Sender != "Gluck" {
		t.Fatalf("segments = %+v", segs)
	}
}
