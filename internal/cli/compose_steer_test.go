package cli

// WHAT THIS FILE ONCE FAILED TO CATCH
//
// Its cases fed WHOLE STRINGS through consume(). A real user types ONE BYTE PER
// READ — a different input path entirely. That hid `composeText += string(b)`
// on a byte, which re-encodes it as a code point and mojibaked every non-ASCII
// character. It also could not see that typing without the trigger key
// discarded input silently, because every case here pressed the trigger first.
//
// Rule: if the thing under test is INPUT, type one character at a time, and
// test the path of someone who does not know the affordance exists.
// See skills/tmux-testing.md.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
)

// The steer composer.
//
// Steering intent cannot be inferred — a prompt pipelined by a script and a
// steer typed by someone watching the stream are byte-identical on the wire, and
// guessing wrong in the merging direction silently swallows a real question. The
// composer is the one place the intent is knowable by construction, so
// everything it submits carries Steering: true.

type steerCapture struct {
	mu    sync.Mutex
	texts []string
	steer []bool
}

func (c *steerCapture) fn(_ context.Context, text string, _ *rpc.ChalkboardInput, steer bool) (int, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, text)
	c.steer = append(c.steer, steer)
	return 0, true, nil
}

func (c *steerCapture) settle(t testing.TB) {
	t.Helper()
	for i := 0; i < 200; i++ {
		c.mu.Lock()
		n := len(c.texts)
		c.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// composeInput builds an input loop in incipit (open=false) or in the pager
// (open=true), with a capturing steer client.
func composeInput(tb testing.TB, open bool) (*interactiveInput, *livelogTurn, *steerCapture) {
	tb.Helper()
	out := &countingWriter{}
	committed := navHistory()
	settings := &renderSettings{}
	status := newSessionStatus("aria0001", time.Unix(0, 0))
	lt := newLivelogTurn(out, 100, 40, settings, "aria0001", time.Unix(0, 0), status, nil, nil)
	if open {
		lt.enterTranscript()
		lt.apply(aria.Page{Parts: committed})
	}
	cap := &steerCapture{}
	return &interactiveInput{
		tc: nil, lt: lt, fcli: stubHistoryClient{committed}, mu: &sync.Mutex{}, set: settings,
		figaroID: "aria0001", cancel: func() {},
		disconnectCh: make(chan struct{}, 1),
		steer:        cap.fn,
	}, lt, cap
}

// The headline: typing into a live view submits a STEER, in both views. Before
// the composer existed the keys went nowhere at all — there was no keymap row
// that composed text, so `figaro send`'s streaming keys were only ^C/^D/^T/^O.
func TestCompose_TypingSubmitsASteer(t *testing.T) {
	for _, tc := range []struct {
		name string
		open bool
	}{{"incipit", false}, {"pager", true}} {
		t.Run(tc.name, func(t *testing.T) {
			in, lt, cap := composeInput(t, tc.open)
			if tc.open {
				in.consume([]byte("i")) // the pager needs a trigger; letters are motions there
			}
			in.consume([]byte("s"))
			if got := lt.transcriptMode(); got != modeCompose {
				t.Fatalf("composing did not start: mode = %v", got)
			}
			in.consume([]byte("ay zucchero\r"))
			cap.settle(t)

			cap.mu.Lock()
			defer cap.mu.Unlock()
			if len(cap.texts) != 1 {
				t.Fatalf("submitted %d prompt(s), want exactly 1", len(cap.texts))
			}
			if cap.texts[0] != "say zucchero" {
				t.Errorf("submitted %q, want %q", cap.texts[0], "say zucchero")
			}
			if !cap.steer[0] {
				t.Error("submitted with Steering=false; a prompt typed into a live view is a steer by construction")
			}
			if lt.transcriptMode() == modeCompose {
				t.Error("composer still open after Enter")
			}
		})
	}
}

// The draft is visible while typing: the footer's status row becomes the draft,
// exactly as it becomes the query line under '/'. One footer, one row, whichever
// mode owns the keyboard.
func TestCompose_DraftShowsInTheFooter(t *testing.T) {
	in, lt, _ := composeInput(t, false)
	in.consume([]byte("abc"))
	line := lt.status.composeLine(80)
	if !strings.Contains(line, "abc") {
		t.Fatalf("footer %q does not show the draft", line)
	}
	if !strings.Contains(line, "steer") {
		t.Errorf("footer %q does not say what the keyboard is doing", line)
	}
	// And the incipit bookend uses the same row.
	rows := bookendLines(lt.status)
	if len(rows) != 2 || !strings.Contains(rows[1], "abc") {
		t.Errorf("incipit bookend does not carry the draft: %q", rows)
	}
}

func TestCompose_BackspaceAndCancel(t *testing.T) {
	in, lt, cap := composeInput(t, false)
	in.consume([]byte("abc\x7f"))
	if got := lt.status.composeLine(80); !strings.Contains(got, "ab") || strings.Contains(got, "abc") {
		t.Fatalf("backspace did not delete one character: %q", got)
	}
	in.consume([]byte("\x1b"))
	if lt.transcriptMode() == modeCompose {
		t.Fatal("Esc did not close the composer")
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.texts) != 0 {
		t.Errorf("Esc submitted %q; cancel must send nothing", cap.texts)
	}
}

// Ctrl-C must still interrupt while composing — the composer may not swallow
// the key that owns the process. Its keymap row is inAnyBox, which now includes
// the compose box.
func TestCompose_CtrlCStillInterrupts(t *testing.T) {
	in, _, _ := composeInput(t, false)
	in.consume([]byte("half typed"))
	_, stop := in.consume([]byte{0x03})
	if !stop {
		t.Fatal("Ctrl-C did not stop the input loop while composing")
	}
}

// A blank draft sends nothing: Enter on an empty box just closes it, rather
// than queueing an empty prompt at the model.
func TestCompose_BlankDraftSendsNothing(t *testing.T) {
	in, lt, cap := composeInput(t, false)
	in.consume([]byte("   \r"))
	if lt.transcriptMode() == modeCompose {
		t.Error("Enter did not close the composer")
	}
	time.Sleep(30 * time.Millisecond)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.texts) != 0 {
		t.Errorf("blank draft submitted %q", cap.texts)
	}
}

// Pager motions are untouched when the composer is closed: 'i' must not have
// stolen a key, and the motions must still move.
//
// The fixture opens pinned to the tail, so 'j' correctly clamps — asserting on
// it would test the clamp, not the binding. 'k' is the unambiguous probe.
func TestCompose_PagerKeysUnaffectedWhenClosed(t *testing.T) {
	in, lt, _ := composeInput(t, true)
	before := lt.tr.offset
	in.consume([]byte("k"))
	if lt.tr.offset >= before {
		t.Fatalf("k no longer scrolls with the composer closed: %d -> %d", before, lt.tr.offset)
	}
	if lt.transcriptMode() == modeCompose {
		t.Fatal("k opened the composer")
	}
}

// A human types one character per read, and a non-ASCII character arrives as
// SEVERAL reads — one per UTF-8 byte. Feeding whole strings through consume()
// (which is what tmux send-keys with a full string does) exercises the one
// input pattern a real user never produces, so both must be tested.
func TestCompose_TypedOneByteAtATime(t *testing.T) {
	const want = "steer me toward zucchero"
	in, lt, cap := composeInput(t, false)
	for _, b := range []byte(want) {
		in.consume([]byte{b}) // one read per keystroke, as a terminal delivers it
	}
	if got := lt.status.composeLine(120); !strings.Contains(got, want) {
		t.Fatalf("draft = %q, want it to contain %q", got, want)
	}
	in.consume([]byte("\r"))
	cap.settle(t)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.texts) != 1 || cap.texts[0] != want {
		t.Fatalf("delivered %q, want [%q]", cap.texts, want)
	}
}

// A multi-byte rune must survive byte-at-a-time entry. string(b) on a byte
// converts it to a CODE POINT and re-encodes, turning 'é' (0xC3 0xA9) into
// "Ã©" — every non-ASCII character became mojibake.
func TestCompose_MultiByteRunesSurviveByteAtATimeEntry(t *testing.T) {
	const want = "café 日本"
	in, lt, _ := composeInput(t, false)
	for _, b := range []byte(want) {
		in.consume([]byte{b})
	}
	if got := lt.status.composeLine(120); !strings.Contains(got, want) {
		t.Fatalf("draft = %q, want it to contain %q", got, want)
	}
	// And backspace is rune-wise: one press removes 本, not a stray byte.
	in.consume([]byte{0x7f})
	if got := lt.status.composeLine(120); !strings.Contains(got, "café 日") || strings.Contains(got, "本") {
		t.Fatalf("after backspace draft = %q, want it to end at %q", got, "café 日")
	}
}

// Backspace typed as its own keystroke, repeatedly, across separate reads.
func TestCompose_BackspaceAcrossReads(t *testing.T) {
	in, lt, _ := composeInput(t, false)
	for _, b := range []byte("abcdef") {
		in.consume([]byte{b})
	}
	for i := 0; i < 3; i++ {
		in.consume([]byte{0x7f})
	}
	if got := lt.status.composeLine(60); !strings.Contains(got, "abc") || strings.Contains(got, "abcd") {
		t.Fatalf("draft = %q, want exactly three characters removed", got)
	}
}

// A draft must never be silently destroyed. When the turn it was aimed at ends
// underneath it, the composer stays open, the process stays up, and the label
// stops claiming "steer" — because the exchange it would have steered is over.
func TestCompose_DraftSurvivesTheEndOfItsTurn(t *testing.T) {
	in, lt, _ := composeInput(t, false)
	for _, b := range []byte("half typed") {
		in.consume([]byte{b})
	}
	if got := lt.status.composeLine(60); !strings.Contains(got, "steer ↳") {
		t.Fatalf("while the turn runs the label must say steer: %q", got)
	}

	if !lt.status.composeTurnEnded() {
		t.Fatal("a non-empty draft must hold the process open when its turn ends")
	}
	if !lt.status.composingNow() {
		t.Error("composer closed when the turn ended; the draft was destroyed")
	}
	got := lt.status.composeLine(60)
	if !strings.Contains(got, "half typed") {
		t.Errorf("draft lost: %q", got)
	}
	if !strings.Contains(got, "send ↳") || strings.Contains(got, "steer ↳") {
		t.Errorf("label still claims steer after the turn ended: %q", got)
	}
}

// ...and sending it then opens a NEW turn rather than being absorbed into the
// finished one. Absorbing it would be I2's inverse bug wearing a new hat: a real
// question merged into an exchange it does not belong to.
func TestCompose_AfterTurnEndSendsAsANewTurnNotASteer(t *testing.T) {
	in, lt, cap := composeInput(t, false)
	for _, b := range []byte("a real question") {
		in.consume([]byte{b})
	}
	lt.status.composeTurnEnded()
	in.consume([]byte("\r"))
	cap.settle(t)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.texts) != 1 || cap.texts[0] != "a real question" {
		t.Fatalf("delivered %q", cap.texts)
	}
	if cap.steer[0] {
		t.Error("sent with Steering=true after its turn ended; that absorbs a question into a finished exchange")
	}
}

// An EMPTY composer must not hold the process open — that would hang a command
// merely because the box was showing.
func TestCompose_EmptyDraftDoesNotHoldTheProcess(t *testing.T) {
	_, lt, _ := composeInput(t, false)
	lt.status.composeOpen(true)
	if lt.status.composeTurnEnded() {
		t.Fatal("an empty composer must not hold the process open")
	}
	if lt.status.composeHeldOpen() {
		t.Error("held flag set for an empty draft")
	}
}

// THE HEADLINE. In the inline view a user just types — no trigger. Requiring one
// silently ate the first word, and because English sentences contain 'i', any
// trigger mid-sentence discarded everything before it and turned the rest into
// the message. "zebra" contains no 'i' and used to vanish entirely.
func TestCompose_AnyPrintableStartsComposingInIncipit(t *testing.T) {
	for _, word := range []string{"zebra", "just do it", "yes please", "/slash", "?ask"} {
		t.Run(word, func(t *testing.T) {
			in, lt, _ := composeInput(t, false)
			for _, b := range []byte(word) {
				in.consume([]byte{b}) // one read per keystroke, as a terminal delivers it
			}
			if lt.transcriptActive() {
				t.Fatalf("typing %q opened the pager instead of composing", word)
			}
			if got := lt.status.composeLine(80); !strings.Contains(got, word) {
				t.Fatalf("draft = %q, want it to contain %q", got, word)
			}
		})
	}
}

// ...and the whole thing is delivered as a steer, first character included.
func TestCompose_InlineTypingDeliversTheWholeText(t *testing.T) {
	const want = "zebra: also say rasoio"
	in, _, cap := composeInput(t, false)
	for _, b := range []byte(want) {
		in.consume([]byte{b})
	}
	in.consume([]byte("\r"))
	cap.settle(t)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.texts) != 1 || cap.texts[0] != want {
		t.Fatalf("delivered %q, want [%q]", cap.texts, want)
	}
	if !cap.steer[0] {
		t.Error("inline typing must submit as a steer")
	}
}

// In the PAGER the letters are motions, so a trigger is the only way in and
// navigation must be untouched.
func TestCompose_PagerKeepsMotionsAndTheTrigger(t *testing.T) {
	in, lt, _ := composeInput(t, true)
	before := lt.tr.offset
	in.consume([]byte("k"))
	if lt.tr.offset >= before {
		t.Fatalf("k stopped navigating in the pager: %d -> %d", before, lt.tr.offset)
	}
	if lt.transcriptMode() == modeCompose {
		t.Fatal("k started composing in the pager; motions must win there")
	}
	in.consume([]byte("i"))
	if lt.transcriptMode() != modeCompose {
		t.Fatal("i no longer opens the composer in the pager")
	}
	// The trigger itself inserts nothing there.
	if got := lt.status.composeLine(40); strings.Contains(got, "i▏") {
		t.Errorf("the pager trigger inserted itself: %q", got)
	}
}

// Control chords are gestures, not text: they must survive in the inline view.
func TestCompose_ControlChordsStillWorkInline(t *testing.T) {
	in, _, _ := composeInput(t, false)
	_, stop := in.consume([]byte{0x03}) // Ctrl-C with nothing composed
	if !stop {
		t.Fatal("Ctrl-C stopped working in incipit")
	}
	in2, lt2, _ := composeInput(t, false)
	in2.consume([]byte{0x14}) // Ctrl-T opens the pager
	if !lt2.transcriptActive() {
		t.Fatal("Ctrl-T stopped opening the pager from incipit")
	}
}
