package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/rpc"
)

// THE PREAMBLE IS PRINTED ONCE, BY HAND, AND NEVER BY THE CLIENT.
//
// `q -- "..."` against an aria with history used to open on a dim rule and
// nothing else: no sign of where you were until the model's first token. The
// obvious fix — apply a catch-up read to the aria.Client — is worse than the
// bug, because OnClosed's inline branch Freezes every message it is handed to
// NATIVE SCROLLBACK. That would re-dump the whole retained history on every
// prompt.
//
// So the page is folded outside the client and printed once, and the freeze
// boundary is seeded from it so that the rest of the session treats it as
// already committed. This test states both halves: the context is on screen,
// and NOTHING reaches scrollback twice — including across a pager round trip,
// which re-flushes everything past that boundary.
func TestOpeningPreamblePrintsRecentContextExactlyOnce(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	// Two prior turns, as the tail read would fold them.
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

	// This session's turn: the question freezes inline under the preamble, the
	// reply streams, and the user takes a pager round trip before it seals.
	lt.apply(inquiryPage(6, "NEWQUESTION"))
	lt.apply(page(6, 0, delta(0, livedoc.RoleOutput, "NEWANSWER")))
	lt.enterTranscript()
	// ^T does a catch-up read, and the page it applies restates the history the
	// preamble already printed AND the turn already streaming. Both must be
	// absorbed silently: the client refuses to re-finalize a turn it has closed,
	// and the flush boundary excludes everything at or below the seed.
	lt.apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 4, Inquiry: "OLDQUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OLDANSWER"}}}},
		{Turn: aria.Turn{ID: 5, Inquiry: "PRIORQUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}},
	}})
	lt.apply(page(6, 1))
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 6, Sealed: true}}}})
	lt.finishTurn("completed")

	// Everything above is already in the terminal's scrollback and the pager's
	// alt-screen restore leaves it alone, so the only thing that can duplicate
	// it is what the EXIT flush prints. That is what this measures — the whole
	// byte stream cannot be counted, because the live region the pager left
	// behind is erased rather than removed.
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

// The question must land UNDER the history it follows. Frames arrive on the
// notify pump as soon as the socket is dialed, so without the hold a fast
// daemon can freeze the prompt before the (later) read lands, and the session
// opens with the new question above last week's answer.
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
	lt.openInline([]aria.Message{{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}})

	got := out.String()
	prior, question := strings.Index(got, "PRIORANSWER"), strings.Index(got, "NEWQUESTION")
	if prior < 0 || question < 0 {
		t.Fatalf("expected both the preamble and the released question:\n%s", got)
	}
	if prior > question {
		t.Fatalf("the question was painted above the history it follows:\n%s", got)
	}
}

// THE CANARY FOR THE SEED. Ctrl-T before the model has closed anything is the
// case that makes the boundary load-bearing: the pager's own catch-up read
// fills the client with HISTORY, and on exit a zero boundary reads as "we
// entered cold" — which bounds the flush to the last turn it can see, i.e. the
// last turn of the history the preamble just printed. Seeding the boundary is
// what tells flushTail that pre-session content is already committed.
func TestPagerExitAfterPreambleDoesNotReprintHistory(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	lt.seedContext([]aria.Message{{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}})

	// The prompt is accepted and the preamble is on screen, but the daemon has
	// not pushed a single frame yet — the window between Qua returning and the
	// turn's first notification. Ctrl-T lands there, and the pager's catch-up
	// read hands the client exactly the history the preamble just printed.
	lt.enterTranscript()
	lt.apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 5, Inquiry: "PRIORQUESTION", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}},
	}})
	out.Reset()
	lt.leaveTranscript() // q

	flushed := out.String()
	for _, token := range []string{"PRIORANSWER", "PRIORQUESTION"} {
		if n := bodyCount(flushed, token); n != 0 {
			t.Fatalf("leaving the pager reprinted %s (x%d) under the copy the preamble printed:\n%s", token, n, flushed)
		}
	}
}

// A brand new aria and an ephemeral one have no history at all: the read comes
// back empty, and the session must open EXACTLY as it did before — no rows, and
// no freeze boundary (a zero boundary is what makes a cold pager exit bound
// itself to the last turn).
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

// The pager renders history itself and owns the alternate screen; printing a
// preamble under it would both duplicate that history and write rows the
// alt-screen restore then discards. --listen enters the pager before the
// prompt is even sent, so this case is reachable in one flag.
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

// Orientation, not a transcript: the preamble may not push the question the
// user just asked off the top of the screen. A single enormous prior turn is
// clipped from the HEAD — the rows nearest the new prompt are the ones worth
// keeping — and the boundary is still seeded from the WHOLE message, so the
// elided rows can never arrive later.
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

	// A pane with no room for a reply falls back to the question alone rather
	// than overrunning: knowing WHICH conversation you are in is most of the
	// value, and the pager is one keystroke away for the rest.
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

// What the preamble is allowed to show: sealed turns only, and nothing past the
// cursor Qua reported. The first keeps a turn another client is mid-way through
// from being half-printed as history; the second keeps THIS prompt out of its
// own preamble — the read races the daemon committing the inquiry we just sent.
func TestRecentContextShowsOnlySealedTurnsAtOrBelowTheCursor(t *testing.T) {
	rc := &fakeRecentReader{page: aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 4, Inquiry: "old", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "SEALEDOLD"}}}},
		{Turn: aria.Turn{ID: 5, Inquiry: "running",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "INFLIGHT"}}}},
		{Turn: aria.Turn{ID: 6, Inquiry: "mine", Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OWNTURN"}}}},
	}}}

	got := recentContext(context.Background(), rc, 5)

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

// A daemon that is slow, wedged, or speaking an older wire must cost the prompt
// nothing but the timeout: no error, no output, and a session that opens the
// way it always did.
func TestRecentContextFailsQuietly(t *testing.T) {
	if got := recentContext(context.Background(), &fakeRecentReader{err: errors.New("nope")}, 9); got != nil {
		t.Fatalf("a failed catch-up returned %+v", got)
	}
	if got := recentContext(context.Background(), &fakeRecentReader{}, 9); len(got) != 0 {
		t.Fatalf("an empty aria returned %+v", got)
	}
}

type fakeRecentReader struct {
	page   aria.Page
	err    error
	anchor aria.Anchor
	budget int
}

func (f *fakeRecentReader) Read(context.Context, int) (aria.Page, error) { return aria.Page{}, nil }

func (f *fakeRecentReader) ReadBefore(_ context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	f.anchor, f.budget = at, budget
	return f.page, f.err
}

func (f *fakeRecentReader) Queued(context.Context) (*rpc.QueuedResponse, error) { return nil, nil }

// bodyCount counts the rows whose printable text is exactly token — the only
// sound way to count a token in rendered output (a substring count also finds
// it in a footer, a mantra, or a wrapped fragment).
func bodyCount(out, token string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(stripANSI(line)) == token {
			n++
		}
	}
	return n
}

// THE FOOTER IS ARMED BEFORE THE HELD FRAMES LAND, AND THIS IS WHY.
//
// The status footer is pinned the instant a submit is accepted, and the turn's
// first frame ADOPTS that region in place. Release the held frames first and
// the question paints as one live region with the footer opening a SECOND one
// below it: two status bars on screen, and a stale region that the pager's
// exit erase then misses — after which the question reaches scrollback twice.
// A pty found that; every unit assertion in this file was green through it.
func TestReleasedFramesAdoptTheArmedFooter(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 40)
	status := newSessionStatus("aria1234", time.Now())
	bookend := func() []string { return []string{"BOOKENDROW"} }
	lt := newLivelogTurn(ft, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, bookend, dimRule)
	lt.openRule()

	lt.holdFrames()
	lt.armThinking() // pinned at submit, before anything about the turn is known
	lt.apply(inquiryPage(6, "NEWQUESTION"))
	lt.openInline([]aria.Message{{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}}})

	screen := strings.Join(ft.Screen(), "\n")
	if n := bodyCount(screen, "BOOKENDROW"); n != 1 {
		t.Fatalf("%d live footers on screen, want 1:\n%s", n, screen)
	}
	prior, question := strings.Index(screen, "PRIORANSWER"), strings.Index(screen, "NEWQUESTION")
	if prior < 0 || question < 0 || prior > question {
		t.Fatalf("preamble and question out of order (prior=%d question=%d):\n%s", prior, question, screen)
	}
}

// THE DISCRIMINATOR IS THE EVENT, NOT THE STATE.
//
// Whether to orient the user is the question "am I watching a turn I did not
// open?", and the honest answer is on the wire: the daemon records the inquiry
// verbatim (OpenInquiry(a.turnID, prompt.text)) and RETURNS BEFORE IT when the
// prompt is a steer, so a turn we merely joined never broadcasts a question of
// ours. Qua's `active` flag is sampled before the prompt is even queued, so it
// can be stale in both directions.
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

// The wait has three exits and each one has to mean something different: our
// question came back (ours — say nothing), the agent errored (no turn is
// coming — orient), or nobody said anything in time (we are watching someone
// else's turn — orient). The deadline exists because the steer branch may
// deliver NOTHING for tens of seconds, so waiting on it would hang the prompt.
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

// ONE FETCH, TWO SURFACES.
//
// A prompt that lands on a turn it did not open buys a little context. The
// inline view prints a bounded slice of it, and the PAGER gets the whole page:
// opening it renders that history immediately instead of waiting on a read of
// its own. The page still never reaches aria.Client — that is what keeps
// OnClosed from re-freezing history into scrollback — so the pager is fed
// through its own window, and what it already holds must not arrive twice.
func TestJoinedFetchSeedsThePager(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()

	// Someone else's turn is already streaming when we arrive.
	lt.holdFrames()
	lt.apply(inquiryPage(6, "SOMEONE ELSES QUESTION"))
	lt.apply(page(6, 0, delta(0, livedoc.RoleOutput, "JOINEDANSWER")))

	// The catch-up page: two prior turns, plus a restatement of the turn the
	// client is already holding — the tail read reaches it, so the overlap is
	// the normal case, not a contrived one.
	fetched := []aria.Message{
		{Turn: 4, Inquiry: "OLDQUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "OLDANSWER"}}},
		{Turn: 5, Inquiry: "PRIORQUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "PRIORANSWER"}}},
		{Turn: 6, Inquiry: "SOMEONE ELSES QUESTION", Role: livedoc.RoleOutput,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "JOINEDANSWER"}}},
	}
	lt.openInline(fetched)

	if !lt.hasSeed() {
		t.Fatal("the fetch must be kept for the pager, not just printed")
	}
	lt.apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 6, Sealed: true}}}})
	lt.enterTranscript() // Ctrl-T, with no read of its own

	pager := strings.Join(lt.tr.lines(), "\n")
	for _, token := range []string{"OLDANSWER", "PRIORANSWER"} {
		if n := bodyCount(pager, token); n != 1 {
			t.Fatalf("pager shows %s %d times, want 1 (seeded from the catch-up page):\n%s", token, n, pager)
		}
	}
	if n := bodyCount(pager, "JOINEDANSWER"); n != 1 {
		t.Fatalf("pager shows the joined turn %d times, want 1 — the seed overlapped the window:\n%s", n, pager)
	}
}

// The other half of the same contract: a prompt that opened its own turn
// fetches nothing, so there is nothing to seed and the pager must still take
// its own read. (The input loop asks hasSeed() to decide.)
func TestOwnTurnLeavesNothingToSeed(t *testing.T) {
	var out bytes.Buffer
	status := newSessionStatus("aria1234", time.Now())
	lt := newLivelogTurn(&out, 80, 40, &renderSettings{}, "aria1234", time.Now(), status, nil, dimRule)
	lt.openRule()
	lt.holdFrames()
	lt.apply(inquiryPage(6, "OUR OWN QUESTION"))
	lt.openInline(nil)

	if lt.hasSeed() {
		t.Fatal("call-response fetched nothing; there is nothing to seed")
	}
	lt.enterTranscript()
	if lt.tr.seed != nil {
		t.Fatalf("the pager was handed a seed it should not have: %+v", lt.tr.seed)
	}
}
