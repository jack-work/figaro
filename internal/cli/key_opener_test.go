package cli

import "testing"

// ---------------------------------------------------------------------------
// Which keys open the pager from incipit.
//
// opensTranscriptFor is the gate a key must pass to yank the pager up while a
// turn streams inline. The set is deliberately not "every key": see the
// function's own comment for the ones that were left out and why.
// ---------------------------------------------------------------------------

// TestInputConsume_BangOpensStatusPanel: '!' raises the status panel from
// inside the pager.
//
// It used to do so from INCIPIT too. It no longer does, deliberately: in the
// inline view every printable character starts composing a steer, because
// requiring a trigger silently ate the user's first word. '!' is a printable
// character someone may be typing. The pager is reached with ^T or ^L: control
// keys, which are never text.
func TestInputConsume_BangOpensStatusPanel(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, true)
	if rest := feed(t, in, "!"); len(rest) != 0 {
		t.Fatalf("consume held %q", rest)
	}
	if !lt.transcriptActive() {
		t.Fatal("! must keep the transcript pager up")
	}
	if !lt.tr.showStatus {
		t.Fatal("! must raise the figaro status panel")
	}
}

// ...and from incipit it is an opening gesture: the pager comes up and the
// panel acts on arrival, rather than the key looking like a dead keyboard.
func TestInputConsume_BangOpensThePagerFromIncipit(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, false)
	feed(t, in, "!")
	if !lt.transcriptActive() {
		t.Fatal("! no longer opens the pager from incipit")
	}
}

// TestOpensTranscriptFor_RejectedKeys guards the keys we deliberately left out:
// each would open the pager onto a no-op or something actively wrong.
func TestOpensTranscriptFor_RejectedKeys(t *testing.T) {
	for _, b := range []byte{'n', 'N', 'y', 'q', 0x1b, 0x03, 0x04} {
		if opensTranscriptFor(b) {
			t.Fatalf("key %q must not be an opening gesture", b)
		}
	}
	// The panel keys ARE opening gestures: they raise the pager and act.
	for _, b := range []byte{'!', '?', 'Q'} {
		if !opensTranscriptFor(b) {
			t.Fatalf("panel key %q must open the pager from incipit", b)
		}
	}
}
