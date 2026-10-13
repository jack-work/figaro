package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

// Every arrow lands where its letter-key equivalent lands, whether the pager is up
// or has to be opened.

// Only the entry read is served; later page fetches come back empty, so the pager
// stops prefetching.
type stubHistoryClient struct{ committed []aria.TurnPart }

func (c stubHistoryClient) Read(context.Context, aria.Anchor, int) (aria.Page, error) {
	return aria.Page{}, nil
}

func (c stubHistoryClient) ReadBefore(_ context.Context, at aria.Anchor, _ int) (aria.Page, error) {
	before := int(at.Turn)
	if before != recentCursor {
		return aria.Page{}, nil
	}
	return aria.Page{Parts: c.committed}, nil
}

func (c stubHistoryClient) Queued(context.Context) (*rpc.QueuedResponse, error) {
	return &rpc.QueuedResponse{}, nil
}

// Waits out the background fetch the last keypress kicked off.
func settle(tb testing.TB, in *interactiveInput) {
	tb.Helper()
	for {
		in.mu.Lock()
		done := in.pageDone
		in.mu.Unlock()
		if done == nil {
			return
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			tb.Fatal("history prefetch never finished")
		}
	}
}

func feed(tb testing.TB, in *interactiveInput, data string) []byte {
	tb.Helper()
	rest, stop := in.consume([]byte(data))
	if stop {
		tb.Fatalf("consume(%q) stopped the input loop", data)
	}
	settle(tb, in)
	return rest
}

func navHistory() []aria.TurnPart {
	committed := make([]aria.TurnPart, 40)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 20)}}
	}
	return committed
}

// open=true for the already-paging case.
func navInput(tb testing.TB, out *countingWriter, open bool) (*interactiveInput, *livelogTurn) {
	tb.Helper()
	committed := navHistory()
	settings := &renderSettings{}
	status := newSessionStatus("aria0001", time.Unix(0, 0))
	lt := newLivelogTurn(out, 100, 40, settings, "aria0001", time.Unix(0, 0), status, nil, nil)
	if open {
		lt.enterTranscript()
		lt.apply(aria.Page{Parts: committed})
	}
	return &interactiveInput{
		tc: nil, lt: lt, fcli: stubHistoryClient{committed}, mu: new(renderLock), set: settings,
		figaroID: "aria0001", cancel: func() {},
		disconnectCh: make(chan struct{}, 1),
	}, lt
}

func TestInputConsume_NavKeysMatchLetterMotions(t *testing.T) {
	cases := []struct {
		name  string
		seqs  []string // every encoding of the same key
		equiv string   // the letter keys it must be identical to
	}{
		{"up", []string{"\x1b[A", "\x1bOA"}, "k"},
		{"down", []string{"\x1b[B", "\x1bOB"}, "j"},
		{"pageup", []string{"\x1b[5~"}, "u"},
		{"pagedown", []string{"\x1b[6~"}, "d"},
		{"home", []string{"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~"}, "gg"},
		{"end", []string{"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~"}, "G"},
	}
	// Pager-only: in incipit a printable letter composes a steer while an
	// arrow opens the pager.
	for _, open := range []bool{true} {
		mode := "in the pager"
		for _, tc := range cases {
			t.Run(tc.name+" "+strings.ReplaceAll(mode, " ", "-"), func(t *testing.T) {
				var refOut countingWriter
				ref, refLT := navInput(t, &refOut, open)
				if rest := feed(t, ref, tc.equiv); len(rest) != 0 {
					t.Fatalf("reference consume(%q) held %q", tc.equiv, rest)
				}
				if !refLT.transcriptActive() {
					t.Fatalf("reference key %q did not open the pager", tc.equiv)
				}
				for _, seq := range tc.seqs {
					var out countingWriter
					in, lt := navInput(t, &out, open)
					if rest := feed(t, in, seq); len(rest) != 0 {
						t.Fatalf("consume(%q) held %q", seq, rest)
					}
					if !lt.transcriptActive() {
						t.Fatalf("%q must open the transcript pager", seq)
					}
					if lt.tr.offset != refLT.tr.offset {
						t.Fatalf("%q landed at offset %d, %q at %d",
							seq, lt.tr.offset, tc.equiv, refLT.tr.offset)
					}
					if lt.tr.follow != refLT.tr.follow {
						t.Fatalf("%q left follow=%v, %q left %v",
							seq, lt.tr.follow, tc.equiv, refLT.tr.follow)
					}
					if strings.Join(lt.tr.prev, "\n") != strings.Join(refLT.tr.prev, "\n") {
						t.Fatalf("%q painted a different screen than %q", seq, tc.equiv)
					}
				}
			})
		}
	}
}

// The cut starts after the introducer: a lone ESC is resolved as a keypress by
// every decoder, since the input loop has no timer.
func TestInputConsume_NavKeySplitAcrossReads(t *testing.T) {
	for _, seq := range []string{"\x1b[A", "\x1bOB", "\x1b[5~", "\x1b[6~", "\x1b[1~", "\x1b[F"} {
		for cut := 2; cut < len(seq); cut++ {
			t.Run(strings.ReplaceAll(seq, "\x1b", "ESC"), func(t *testing.T) {
				var out countingWriter
				in, lt := navInput(t, &out, true)
				lt.tr.selectNode(1, false) // Esc would clear this; nothing may
				if !lt.tr.selection.active {
					t.Fatal("test setup: expected a live selection")
				}
				before := lt.tr.offset

				rest := feed(t, in, seq[:cut])
				if string(rest) != seq[:cut] {
					t.Fatalf("partial %q held as %q, want the whole prefix", seq[:cut], rest)
				}
				if lt.tr.offset != before {
					t.Fatalf("a partial sequence moved the viewport to %d", lt.tr.offset)
				}
				if !lt.tr.selection.active {
					t.Fatal("a sequence prefix fired the bare-Esc binding")
				}

				if rest = feed(t, in, string(rest)+seq[cut:]); len(rest) != 0 {
					t.Fatalf("completing consume held %q", rest)
				}
				var whole countingWriter
				ref, refLT := navInput(t, &whole, true)
				refLT.tr.selectNode(1, false)
				feed(t, ref, seq)
				if lt.tr.offset != refLT.tr.offset {
					t.Fatalf("split %q landed at %d, whole at %d (cut %d)",
						seq, lt.tr.offset, refLT.tr.offset, cut)
				}
			})
		}
	}
}

func TestInputConsume_UnknownSequenceStillSwallowed(t *testing.T) {
	for _, seq := range []string{"\x1bOP", "\x1b[3~", "\x1b[15~", "\x1b[D", "\x1b[C", "\x1b]0;title\x07"} {
		var out countingWriter
		in, lt := navInput(t, &out, false)
		if rest := feed(t, in, seq); len(rest) != 0 {
			t.Fatalf("consume(%q) held %q", seq, rest)
		}
		if lt.transcriptActive() {
			t.Fatalf("%q must not open the transcript pager", seq)
		}
	}
	// In the pager the same sequences are inert, not read as their trailing letter.
	var out countingWriter
	in, lt := navInput(t, &out, true)
	before := lt.tr.offset
	if rest := feed(t, in, "\x1bOP\x1b[3~\x1b[D"); len(rest) != 0 {
		t.Fatalf("consume held %q", rest)
	}
	if lt.tr.offset != before {
		t.Fatalf("an unclaimed sequence scrolled the pager to %d, want %d", lt.tr.offset, before)
	}
}

// A held arrow autorepeats into one read: one frame, not one per press.
func TestInputConsume_ArrowBurstPaintsOneFrame(t *testing.T) {
	const presses = 12
	var w countingWriter
	in, lt := coalesceInput(t, &w)
	lt.tr.stopFollowing() // measure pure motion: leaving live also reclaims the padding row
	before := lt.tr.offset
	w.reset()
	burst := strings.Repeat("\x1b[A", presses)
	if rest, stop := in.consume([]byte(burst)); stop || len(rest) != 0 {
		t.Fatalf("consume = %q, %v", rest, stop)
	}
	if got := w.writes.Load(); got != 1 {
		t.Fatalf("%d autorepeated Up arrows painted %d frames, want 1", presses, got)
	}
	if lt.tr.offset != before-presses {
		t.Fatalf("offset moved to %d, want %d", lt.tr.offset, before-presses)
	}
}

func TestTranscriptNav_SearchPromptOwnsTheKeyboard(t *testing.T) {
	var w countingWriter
	in, lt := coalesceInput(t, &w)
	if rest, stop := in.consume([]byte("/ab")); stop || len(rest) != 0 {
		t.Fatalf("consume = %q, %v", rest, stop)
	}
	if !lt.tr.inSearch || lt.tr.query != "ab" {
		t.Fatalf("search state = %v, %q", lt.tr.inSearch, lt.tr.query)
	}
	before := lt.tr.offset
	if rest, stop := in.consume([]byte("\x1b[A\x1b[6~")); stop || len(rest) != 0 {
		t.Fatalf("consume = %q, %v", rest, stop)
	}
	if lt.tr.query != "ab" {
		t.Fatalf("arrows typed into the query: %q", lt.tr.query)
	}
	if lt.tr.offset != before {
		t.Fatalf("arrows scrolled behind the search prompt: %d, want %d", lt.tr.offset, before)
	}
}

func TestNavArrowsStillOpenThePagerFromIncipit(t *testing.T) {
	for _, seq := range []string{"\x1b[A", "\x1b[B", "\x1b[5~", "\x1b[6~", "\x1b[H", "\x1b[F"} {
		var out countingWriter
		in, lt := navInput(t, &out, false)
		feed(t, in, seq)
		if !lt.transcriptActive() {
			t.Errorf("%q no longer opens the pager from incipit", seq)
		}
	}
	// Nothing in incipit swallows a keystroke as text.
	var out countingWriter
	in, lt := navInput(t, &out, false)
	feed(t, in, "j")
	if !lt.transcriptActive() {
		t.Error("'j' no longer opens the pager from incipit")
	}
}
