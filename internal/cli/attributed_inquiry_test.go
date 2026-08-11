package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// THE SHAPE, PINNED ACROSS ALL THREE SURFACES.
//
//	> input
//	  aria 123456
//	  Hello
//
//	  Jack
//	  Hello again
//	  ─────
//	< figaro
//	  Hello to both of you
//
// ONE "> input" for the whole question however many people wrote it: the
// submissions folded into one message and a header apiece would say otherwise.
// Each segment is prefaced by its sender, with a blank line between segments so
// the parts read as separate messages.
//
// All three surfaces must agree, for the reason inquiry_chrome_test.go already
// gives: a live-vs-committed difference here is not cosmetics, it is the same
// exchange telling two stories about who spoke.
func TestAttributedInquiryShapeAgreesAcrossViews(t *testing.T) {
	segs := []aria.InquirySegment{
		{Sender: "aria 123456", Text: "Hello"},
		{Sender: "Jack", Text: "Hello again"},
	}
	const joined = "Hello\n\nHello again"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "Hello to both of you"}}
	want := []string{
		"> input", "",
		"aria 123456", "Hello", "",
		"Jack", "Hello again",
		"", "─", "< figaro", "", "Hello to both of you",
	}

	t.Run("show", func(t *testing.T) {
		assertChrome(t, renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: joined, InquirySegments: segs, Nodes: nodes}, 48, 0, renderSettings{}), want)
	})

	t.Run("pager", func(t *testing.T) {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{{
			Turn: aria.Turn{
				ID: 1, Inquiry: joined, InquirySegments: segs,
				Sealed: true, Nodes: nodes,
			},
		}}})
		ft := ldrender.NewFakeTerminal(48, 24)
		tr := newTranscript(ft, 48, 24, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		tr.follow = false
		assertChrome(t, tr.lines(), want)
	})

	t.Run("inline", func(t *testing.T) {
		ft := ldrender.NewFakeTerminal(48, 24)
		in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
		in.Header = messageHeader
		in.Sender = dimSender
		in.Rule = func() string { return strings.Repeat("─", 48) }
		m := aria.Message{
			Turn: 1, Inquiry: joined, InquirySegments: segs,
			Role: livedoc.RoleOutput, Nodes: nodes,
		}
		in.Open(m)
		in.Freeze(m)
		assertChrome(t, ft.Screen(), want)
	})
}

// An UNATTRIBUTED question must render EXACTLY as it did before senders
// existed: no blank row where a sender would go, no "unknown". Most messages
// ever written have no sender, and a placeholder on each of them would be noise
// where there used to be none.
func TestUnattributedInquiryIsUnchanged(t *testing.T) {
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	want := []string{"> input", "", "THEQUESTION", "", "─", "< figaro", "", "THEANSWER"}

	withSegs := renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "THEQUESTION", InquirySegments: nil, Nodes: nodes}, 48, 0, renderSettings{})
	assertChrome(t, withSegs, want)

	// A segment list carrying no senders is the same thing said differently,
	// and must render identically: otherwise the presence of the field, not
	// the presence of a sender, would change the screen.
	blank := []aria.InquirySegment{{Text: "THEQUESTION"}}
	assertChrome(t, renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "THEQUESTION", InquirySegments: blank, Nodes: nodes}, 48, 0, renderSettings{}), want)
}
