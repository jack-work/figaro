package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func quotedQuestionPager(t testing.TB, ask string) *transcript {
	t.Helper()
	inquiry := "> quoting aria bd0cf2bd · turn 3 · 40 chars\n> the passage being asked about\n> and its second line"
	if ask != "" {
		inquiry += "\n\n" + ask
	}
	var nodes []livedoc.Node
	for n := range 20 {
		nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("NODE%d", n)})
	}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: inquiry,
		InquirySegments: []aria.InquirySegment{{Sender: "Duke", Text: inquiry}},
		Nodes:           nodes,
	}}}}, aria.Notify)
	view := &ariaView{settings: &renderSettings{sticky: true}}
	tr := newTranscript(ldrender.NewFakeTerminal(64, 16), 64, 16, view, client, "quoted01", time.Time{})
	tr.enter()
	tr.follow = false
	tr.buildIndex()
	return tr
}

func scrolledPastQuestion(tr *transcript) []string {
	tr.offset = len(tr.stickyBlockOf(1).rows) + 1
	tr.buildIndex()
	return plain(headRowsOf(tr))
}

// Defends the ask: a quoted question pinned its quote card's heading, which
// says nothing about what was asked.
func TestSticky_QuotedQuestionPinsTheAskersWords(t *testing.T) {
	tr := quotedQuestionPager(t, "what do you make of this passage?")
	head := scrolledPastQuestion(tr)
	if len(head) != stickyText+1 {
		t.Fatalf("the header is %d rows, want %d and its rule:\n%s", len(head), stickyText, strings.Join(head, "\n"))
	}
	if !strings.HasPrefix(head[0], "∨") || !strings.HasSuffix(head[0], "1") {
		t.Fatalf("the heading row lost its glyph or its coordinate: %q", head[0])
	}
	if !strings.HasPrefix(head[1], quoteMark) || !strings.Contains(head[1], "what do you make of this passage?") {
		t.Fatalf("the header does not show the asker's words under a quote sigil: %q", head[1])
	}
	if strings.Contains(strings.Join(head, "\n"), "quoted") {
		t.Fatalf("the quote card's heading is still pinned:\n%s", strings.Join(head, "\n"))
	}
}

func TestSticky_QuotedQuestionLetsGoToItsFullCard(t *testing.T) {
	tr := quotedQuestionPager(t, "what do you make of this passage?")
	tr.offset = 0
	tr.buildIndex()
	body := strings.Join(plain(bodyRows(tr)), "\n")
	if !strings.Contains(body, quoteOpen+" "+quoteMark) || !strings.Contains(body, "the passage being asked about") {
		t.Fatalf("the block lost its quote card:\n%s", body)
	}
}

func TestSticky_QuoteAloneStillPinsTheCard(t *testing.T) {
	tr := quotedQuestionPager(t, "")
	head := scrolledPastQuestion(tr)
	if len(head) < 2 || !strings.Contains(head[1], quoteOpen+" "+quoteMark) {
		t.Fatalf("a question that is only a quote has no words of its own to pin:\n%s", strings.Join(head, "\n"))
	}
}

func TestSticky_QuotedQuestionTooTallIsClipped(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("a very long question that will wrap across several rows ", 4))
	tr := quotedQuestionPager(t, long)
	head := scrolledPastQuestion(tr)
	if len(head) < 2 || !strings.HasSuffix(head[1], strings.TrimSpace(stickyEllipsis)) {
		t.Fatalf("the asker's words overflow the header unmarked:\n%s", strings.Join(head, "\n"))
	}
	short := quotedQuestionPager(t, "short")
	if head := scrolledPastQuestion(short); strings.HasSuffix(head[1], strings.TrimSpace(stickyEllipsis)) {
		t.Fatalf("a one-row question is marked as clipped: %q", head[1])
	}
}
