package turns

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// The transcript shows that a picture came with the question, never its bytes.
func TestInquiryNamesPromptImagesWithoutTheirBytes(t *testing.T) {
	m := message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/jpeg", "SECRETBYTES"),
		message.TextContent("total?"),
		message.ToolImageContent("c1", "read", "image/png", "TOOLBYTES"),
	}}
	got := Inquiry(Text(m), m)
	if got != "[image: image/jpeg]\n\ntotal?" {
		t.Fatalf("inquiry = %q", got)
	}
	if strings.Contains(got, "BYTES") {
		t.Fatal("the inquiry carries image data")
	}
	plain := message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}}
	if Inquiry(Text(plain), plain) != "hi" {
		t.Fatal("a prompt with no picture must read as before")
	}
}

// Defends #114: a later reader never takes a replayed turn, with its stubbed
// tools, for one that ran for real.
func TestAReplayedInquiryCarriesItsMark(t *testing.T) {
	m := message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("second question")},
		Replay: &message.Replay{Of: "abc", Turn: 2, Clock: true, Tools: true}}
	if got := Inquiry(Text(m), m); got != "[replay of abc:2; masked: clock, tools]\n\nsecond question" {
		t.Fatalf("inquiry = %q", got)
	}
	m.Replay = &message.Replay{Of: "abc", Turn: 2}
	if got := Inquiry(Text(m), m); !strings.HasPrefix(got, "[replay of abc:2; nothing masked]") {
		t.Fatalf("inquiry = %q", got)
	}
}
