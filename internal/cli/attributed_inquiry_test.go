package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func TestAttributedInquiryShapeAgreesAcrossViews(t *testing.T) {
	segs := []aria.InquirySegment{
		{Sender: "Ann", Text: "Hello"},
		{Sender: "Jack", Text: "Hello again"},
	}
	const joined = "Hello\n\nHello again"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "Hello to both of you"}}
	want := []string{
		// The heading and its first text row are the pair the pager pins.
		"> Ann", "Hello", "",
		"> Jack", "Hello again",
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
		}}}, aria.Notify)
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
		in.InputHeader = inputHeader
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

// An unattributed question renders with no sender row and no placeholder.
func TestUnattributedInquiryIsUnchanged(t *testing.T) {
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	want := []string{"> input", "THEQUESTION", "", "─", "< figaro", "", "THEANSWER"}

	withSegs := renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "THEQUESTION", InquirySegments: nil, Nodes: nodes}, 48, 0, renderSettings{})
	assertChrome(t, withSegs, want)

	blank := []aria.InquirySegment{{Text: "THEQUESTION"}}
	assertChrome(t, renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "THEQUESTION", InquirySegments: blank, Nodes: nodes}, 48, 0, renderSettings{}), want)
}
