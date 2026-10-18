package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// Each submission names its sender in the heading, not on a second body
// row. Mixed senders retain their own headings within the same inquiry.
func TestAttributedInquiryShapeAgreesAcrossViews(t *testing.T) {
	segs := []aria.InquirySegment{
		{Sender: "aria 123456", Text: "Hello"},
		{Sender: "Jack", Text: "Hello again"},
	}
	const joined = "Hello\n\nHello again"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "Hello to both of you"}}
	want := []string{
		// A heading sits ON its text: the pair is the block's head, and the
		// pager pins exactly those two rows.
		"> figaro 123456", "Hello", "",
		"> Jack", "Hello again",
		// The question closes with one blank row, then the rule.
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

// An UNATTRIBUTED question must render EXACTLY as it did before senders
// existed: no blank row where a sender would go, no "unknown". Most messages
// ever written have no sender, and a placeholder on each of them would be noise
// where there used to be none.
func TestUnattributedInquiryIsUnchanged(t *testing.T) {
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}
	want := []string{"> input", "THEQUESTION", "", "─", "< figaro", "", "THEANSWER"}

	withSegs := renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "THEQUESTION", InquirySegments: nil, Nodes: nodes}, 48, 0, renderSettings{})
	assertChrome(t, withSegs, want)

	// A segment list carrying no senders is the same thing said differently,
	// and must render identically: otherwise the presence of the field, not
	// the presence of a sender, would change the screen.
	blank := []aria.InquirySegment{{Text: "THEQUESTION"}}
	assertChrome(t, renderTurnRows(aria.Message{Role: livedoc.RoleOutput, Inquiry: "THEQUESTION", InquirySegments: blank, Nodes: nodes}, 48, 0, renderSettings{}), want)
}

// An aria that sends a prompt is named the way fig ls names it, never by its
// 32-hex id: the daemon's unique handle when the page carries one, else the
// two-word handle of the id. A human label is shown as given.
func TestAnAriaSenderIsNamedNotNumbered(t *testing.T) {
	const id = "a5647bfbc216b8a09ffea9586393c289"
	segs := []aria.InquirySegment{
		{Sender: id, Name: "named-by-daemon", Text: "first"},
		{Sender: "aria " + id, Text: "second"},
		{Sender: "Jack", Text: "third"},
	}
	const joined = "first\n\nsecond\n\nthird"
	nodes := []livedoc.Node{
		{Type: livedoc.NodeSteering, Sender: id, SenderName: "named-by-daemon", Markdown: "steer"},
		{Type: livedoc.NodeSteering, Sender: id, Markdown: "steer again"},
	}
	words := handle.Words(id, handle.MinWords)
	wantHeads := []string{"> figaro named-by-daemon", "> figaro " + words, "> Jack", "↳ input · figaro named-by-daemon", "↳ input · figaro " + words}

	check := func(t *testing.T, rows []string) {
		t.Helper()
		screen := strings.Join(rows, "\n")
		for _, h := range wantHeads {
			if !strings.Contains(screen, h) {
				t.Errorf("missing %q in\n%s", h, screen)
			}
		}
		if strings.Contains(screen, id[:12]) {
			t.Errorf("the hex id is on screen:\n%s", screen)
		}
	}
	m := aria.Message{Role: livedoc.RoleOutput, Inquiry: joined, InquirySegments: segs, Nodes: nodes}
	t.Run("show", func(t *testing.T) { check(t, renderTurnRows(m, 60, 0, renderSettings{})) })
	t.Run("pager", func(t *testing.T) {
		client := aria.NewClient()
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Inquiry: joined, InquirySegments: segs, Sealed: true, Nodes: nodes}}}}, aria.Notify)
		ft := ldrender.NewFakeTerminal(60, 40)
		tr := newTranscript(ft, 60, 40, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))
		tr.enter()
		tr.follow = false
		check(t, tr.lines())
	})
	t.Run("inline", func(t *testing.T) {
		ft := ldrender.NewFakeTerminal(60, 40)
		in := ldrender.NewIncipit(ft, &ariaView{settings: &renderSettings{}})
		in.Header = messageHeader
		in.InputHeader = inputHeader
		in.Rule = func() string { return strings.Repeat("─", 60) }
		m := m
		m.Turn = 1
		in.Open(m)
		in.Freeze(m)
		check(t, ft.Screen())
	})
}

// The raw view is where a reader goes for the id itself: it names every
// sender of an input message in full.
func TestTheRawViewKeepsTheSendersFullID(t *testing.T) {
	const id = "a5647bfbc216b8a09ffea9586393c289"
	m := message.Message{Role: message.RoleInput, Content: []message.Content{
		message.SenderText(id, "first"), message.SenderText("Jack", "second"),
	}}
	var verbose, plain strings.Builder
	renderMessage(&verbose, m, 4, true)
	renderMessage(&plain, m, 4, false)
	if !strings.Contains(verbose.String(), "**input** [#4] from `"+id+"`, `Jack`") {
		t.Fatalf("verbose input header lost the senders:\n%s", verbose.String())
	}
	if strings.Contains(plain.String(), id) {
		t.Fatalf("the plain view prints the id:\n%s", plain.String())
	}
}
