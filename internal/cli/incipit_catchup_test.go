package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func sealedPage(msgs ...aria.Message) aria.Page {
	var p aria.Page
	for _, m := range msgs {
		p.Parts = append(p.Parts, aria.TurnPart{Turn: aria.Turn{
			ID: uint64(m.Turn), Inquiry: m.Inquiry, Sealed: true, Nodes: m.Nodes,
		}})
	}
	return p
}

// The preamble is folded outside the client (OnClosed freezes every message
// it is handed to scrollback) and the freeze boundary is seeded from it.
func TestOpeningPreamblePrintsRecentContextExactlyOnce(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	history := []aria.Message{
		{Turn: 4, Inquiry: "OLDQUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OLDANSWER"}}},
		{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}},
	}
	lt.seedContext(history)

	if got := bodyCount(out.String(), "PRIORANSWER"); got != 1 {
		t.Fatalf("prior context not rendered on open (PRIORANSWER x%d):\n%s", got, out.String())
	}
	if lt.lastFrozen != (sliceCursor{turn: 5, from: 0}) {
		t.Fatalf("freeze boundary not seeded from the newest history slice: %+v", lt.lastFrozen)
	}

	lt.apply(inquiryPage(6, "NEWQUESTION"))
	lt.apply(page(6, 0, delta(0, livedoc.RoleOutput, "NEWANSWER")))
	lt.enterTranscript()
	// ^T's catch-up read restates the preamble's history and the streaming turn.
	lt.apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 4, Inquiry: "OLDQUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OLDANSWER"}}}},
		{Turn: aria.Turn{ID: 5, Inquiry: "PRIORQUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}},
	}})
	lt.apply(page(6, 1))
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 6, Sealed: true}}}})
	lt.finishTurn("completed")

	// Only the exit flush can duplicate what is already in scrollback.
	out.Reset()
	lt.leaveTranscript()

	flushed := out.String()
	for _, token := range []string{"OLDANSWER", "OLDQUESTION", "PRIORANSWER", "PRIORQUESTION"} {
		if n := bodyCount(flushed, token); n != 0 {
			t.Fatalf("leaving the pager reprinted %s (x%d) \u2014 it is already in scrollback:\n%s", token, n, flushed)
		}
	}
	if n := bodyCount(flushed, "NEWANSWER"); n != 1 {
		t.Fatalf("this session's reply reached scrollback %d times, want 1:\n%s", n, flushed)
	}
}

// Frames arrive as soon as the socket is dialed, so without the hold a fast
// daemon freezes the question above the preamble.
func TestHeldFramesLandAfterThePreamble(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	lt.holdFrames()
	lt.apply(inquiryPage(6, "NEWQUESTION"))
	if strings.Contains(out.String(), "NEWQUESTION") {
		t.Fatalf("a held frame painted anyway:\n%s", out.String())
	}
	lt.openInline(sealedPage(aria.Message{Turn: 5, Inquiry: "PRIORQUESTION",
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}))

	got := out.String()
	prior, question := strings.Index(got, "PRIORANSWER"), strings.Index(got, "NEWQUESTION")
	if prior < 0 || question < 0 {
		t.Fatalf("expected both the preamble and the released question:\n%s", got)
	}
	if prior > question {
		t.Fatalf("the question was painted above the history it follows:\n%s", got)
	}
}

// On exit a zero boundary reads as a cold entry and flushes the last turn
// the pager can see, which is the preamble's.
func TestPagerExitAfterPreambleDoesNotReprintHistory(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	lt.seedContext([]aria.Message{{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}})

	// Ctrl-T before the daemon has pushed a frame.
	lt.enterTranscript()
	lt.apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 5, Inquiry: "PRIORQUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}},
	}})
	out.Reset()
	lt.leaveTranscript()

	flushed := out.String()
	for _, token := range []string{"PRIORANSWER", "PRIORQUESTION"} {
		if n := bodyCount(flushed, token); n != 0 {
			t.Fatalf("leaving the pager reprinted %s (x%d) under the copy the preamble printed:\n%s", token, n, flushed)
		}
	}
}

// An empty read leaves no rows and no freeze boundary.
func TestOpeningPreambleWithNoHistoryIsANoOp(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()
	before := out.String()

	lt.seedContext(nil)

	if out.String() != before {
		t.Fatalf("an empty catch-up printed something:\n%q", out.String()[len(before):])
	}
	if lt.lastFrozen != (sliceCursor{}) {
		t.Fatalf("an empty catch-up moved the freeze boundary: %+v", lt.lastFrozen)
	}
}

// Rows printed under the alt screen are discarded by its restore.
func TestOpeningPreambleSkippedInThePager(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.enterTranscript()
	out.Reset()

	lt.seedContext([]aria.Message{{Turn: 5, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}})

	if strings.Contains(out.String(), "PRIORANSWER") {
		t.Fatalf("the preamble printed under the pager:\n%s", out.String())
	}
	if lt.lastFrozen != (sliceCursor{}) {
		t.Fatalf("the pager's own history moved the inline freeze boundary: %+v", lt.lastFrozen)
	}
}

// A prior turn is clipped from the head; the boundary is still seeded from
// the whole message, so elided rows never arrive later.
func TestOpeningPreambleIsBoundedByTheViewport(t *testing.T) {
	tall := func(last string) []aria.Message {
		var nodes []livedoc.Node
		for k := 0; k < 40; k++ {
			md := "FILLER"
			if k == 39 {
				md = last
			}
			nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: md})
		}
		return []aria.Message{{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput, Nodes: nodes}}
	}

	t.Run("clips the head, keeps the tail", func(t *testing.T) {
		var out bytes.Buffer
		status := newSessionStatus("aria1234", time.Now())
		lt := newLivelogTurn(&out, 80, 60, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
		lt.openRule()

		lt.seedContext(tall("LASTBLOCK"))

		if rows := strings.Count(out.String(), "\n"); rows > 30 {
			t.Fatalf("preamble took %d rows of a 60-row viewport (budget 30):\n%s", rows, out.String())
		}
		if !strings.Contains(out.String(), "LASTBLOCK") {
			t.Fatalf("clipping dropped the tail instead of the head:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "PRIORQUESTION") {
			t.Fatalf("clipping dropped the question, which is the most orienting row there is:\n%s", out.String())
		}
		if lt.lastFrozen != (sliceCursor{turn: 5, from: 0}) {
			t.Fatalf("clipping moved the freeze boundary to %+v; the elided rows would replay", lt.lastFrozen)
		}
	})

	t.Run("a short pane keeps the question", func(t *testing.T) {
		var out bytes.Buffer
		status := newSessionStatus("aria1234", time.Now())
		lt := newLivelogTurn(&out, 80, 20, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
		lt.openRule()

		lt.seedContext(tall("LASTBLOCK"))

		if rows := strings.Count(out.String(), "\n"); rows > 10 {
			t.Fatalf("preamble took %d rows of a 20-row viewport (budget 10):\n%s", rows, out.String())
		}
		if !strings.Contains(out.String(), "PRIORQUESTION") {
			t.Fatalf("nothing orienting survived the clip:\n%s", out.String())
		}
	})
}

// The read races the daemon committing the inquiry just sent, so nothing past
// the reported cursor may show.
func TestRecentContextShowsOnlySealedTurnsAtOrBelowTheCursor(t *testing.T) {
	rc := &fakeRecentReader{page: aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 4, Inquiry: "old", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "SEALEDOLD"}}}},
		{Turn: aria.Turn{ID: 5, Inquiry: "running",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "INFLIGHT"}}}},
		{Turn: aria.Turn{ID: 6, Inquiry: "mine", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OWNTURN"}}}},
	}}}

	got := pageMessages(recentContext(context.Background(), rc, 5))

	if len(got) != 1 || got[0].Turn != 4 {
		t.Fatalf("want only the sealed turn 4, got %+v", got)
	}
	if rc.anchor.Turn != recentCursor {
		t.Fatalf("catch-up read anchored at %+v, want the tail cursor", rc.anchor)
	}
	if rc.budget != wireBudget(recentContextMessages) {
		t.Fatalf("catch-up budget %d is not the wire byte budget", rc.budget)
	}
}

func TestRecentContextFailsQuietly(t *testing.T) {
	if got := recentContext(context.Background(), &fakeRecentReader{err: errors.New("nope")}, 9); len(got.Parts) != 0 {
		t.Fatalf("a failed catch-up returned %+v", got)
	}
	if got := recentContext(context.Background(), &fakeRecentReader{}, 9); len(got.Parts) != 0 {
		t.Fatalf("an empty aria returned %+v", got)
	}
}

type fakeRecentReader struct {
	page   aria.Page
	err    error
	anchor aria.Anchor
	budget int
}

func (f *fakeRecentReader) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (f *fakeRecentReader) ReadBefore(_ context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	f.anchor, f.budget = at, budget
	return f.page, f.err
}

func (f *fakeRecentReader) Queued(context.Context) (*rpc.QueuedResponse, error) { return nil, nil }

// bodyCount counts whole rows: a substring count also finds the token in a
// footer, a mantra, or a wrapped fragment.
func bodyCount(out, token string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(stripANSI(line)) == token {
			n++
		}
	}
	return n
}

// The footer is armed at submit and the turn's first frame adopts that
// region; releasing held frames first opens a second region below it.
func TestReleasedFramesAdoptTheArmedFooter(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 40)
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() []string { return []string{"BOOKENDROW"} }
	lt := newLivelogTurn(ft, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, bookend, dimRule)
	lt.openRule()

	lt.holdFrames()
	lt.armThinking()
	lt.apply(inquiryPage(6, "NEWQUESTION"))
	lt.openInline(sealedPage(aria.Message{Turn: 5, Inquiry: "PRIORQUESTION",
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}))

	screen := strings.Join(ft.Screen(), "\n")
	if n := bodyCount(screen, "BOOKENDROW"); n != 1 {
		t.Fatalf("%d live footers on screen, want 1:\n%s", n, screen)
	}
	prior, question := strings.Index(screen, "PRIORANSWER"), strings.Index(screen, "NEWQUESTION")
	if prior < 0 || question < 0 || prior > question {
		t.Fatalf("preamble and question out of order (prior=%d question=%d):\n%s", prior, question, screen)
	}
}

// A steer returns before OpenInquiry, so a joined turn never broadcasts a
// question of ours; Qua's `active` flag is stale in both directions.
func TestPageCarriesInquiry(t *testing.T) {
	const mine = "Reply with exactly one word: DATE"
	cases := []struct {
		name   string
		page   aria.Page
		prompt string
		want   bool
	}{
		{"our own question opens the turn", inquiryPage(7, mine), mine, true},
		{"whitespace is not a difference", inquiryPage(7, "  "+mine+"\n"), mine, true},
		{"someone else's question is not ours", inquiryPage(7, "an unrelated prompt"), mine, false},
		{"a steer broadcasts no inquiry at all", page(7, 0, delta(0, livedoc.RoleOutput, mine)), mine, false},
		{"an empty prompt matches nothing", inquiryPage(7, ""), "", false},
		{"the inquiry may ride with other parts", aria.Page{Parts: []aria.TurnPart{
			{Turn: aria.Turn{ID: 6, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "old"}}}},
			{Turn: aria.Turn{ID: 7, Inquiry: mine}},
		}}, mine, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pageCarriesInquiry(c.page, c.prompt); got != c.want {
				t.Fatalf("pageCarriesInquiry = %v, want %v", got, c.want)
			}
		})
	}
}

// The steer branch may deliver nothing for tens of seconds, hence the deadline.
func TestAwaitOwnTurn(t *testing.T) {
	closed := func() chan struct{} { c := make(chan struct{}); close(c); return c }

	if !awaitOwnTurn("a question", closed(), make(chan struct{})) {
		t.Fatal("our own inquiry arriving must count as ours")
	}
	if awaitOwnTurn("a question", make(chan struct{}), closed()) {
		t.Fatal("an error means no turn of ours will open")
	}
	if !awaitOwnTurn("   ", make(chan struct{}), make(chan struct{})) {
		t.Fatal("an empty prompt has no inquiry to match; it must not stall on the deadline")
	}

	start := time.Now()
	if awaitOwnTurn("a question", make(chan struct{}), make(chan struct{})) {
		t.Fatal("silence must resolve to 'not ours'")
	}
	if waited := time.Since(start); waited < ownTurnDeadline || waited > ownTurnDeadline*4 {
		t.Fatalf("waited %s, want ~%s", waited, ownTurnDeadline)
	}
}

// The fetched page never reaches aria.Client (OnClosed would re-freeze it);
// the pager is fed through its own window.
func TestJoinedFetchSeedsThePager(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	lt.holdFrames()
	lt.apply(inquiryPage(6, "SOMEONE ELSES QUESTION"))
	lt.apply(page(6, 0, delta(0, livedoc.RoleOutput, "JOINEDANSWER")))

	// The tail read overlaps the turn the client already holds.
	fetched := []aria.Message{
		{Turn: 4, Inquiry: "OLDQUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OLDANSWER"}}},
		{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}},
		{Turn: 6, Inquiry: "SOMEONE ELSES QUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "JOINEDANSWER"}}},
	}
	lt.openInline(sealedPage(fetched...))

	if !lt.hasSeed() {
		t.Fatal("the fetch must be kept for the pager, not just printed")
	}
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 6, Sealed: true}}}})
	lt.enterTranscript()

	pager := strings.Join(lt.tr.lines(), "\n")
	for _, token := range []string{"OLDANSWER", "PRIORANSWER"} {
		if n := bodyCount(pager, token); n != 1 {
			t.Fatalf("pager shows %s %d times, want 1 (seeded from the catch-up page):\n%s", token, n, pager)
		}
	}
	if n := bodyCount(pager, "JOINEDANSWER"); n != 1 {
		t.Fatalf("pager shows the joined turn %d times, want 1: the seed overlapped the window:\n%s", n, pager)
	}
}

func TestOwnTurnLeavesNothingToSeed(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()
	lt.holdFrames()
	lt.apply(inquiryPage(6, "OUR OWN QUESTION"))
	lt.openInline(aria.Page{})

	if lt.hasSeed() {
		t.Fatal("call-response fetched nothing; there is nothing to seed")
	}
	lt.enterTranscript()
	if lt.hasSeed() {
		t.Fatalf("the pager was handed a seed it should not have: %+v", lt.seeded)
	}
}
