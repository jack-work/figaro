package cli

import "testing"

// ---------------------------------------------------------------------------
// Which keys open the pager from incipit.
//
// opensTranscriptFor is the gate a key must pass to yank the pager up while a
// turn streams inline. The set is deliberately not "every key": see the
// function's own comment for the ones that were left out and why.
// ---------------------------------------------------------------------------

// TestInputConsume_BangOpensStatusPanel: '!' worked inside the pager but could
// not get you there — the one plain inconsistency in opensTranscriptFor.
func TestInputConsume_BangOpensStatusPanel(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, false)
	if rest := feed(t, in, "!"); len(rest) != 0 {
		t.Fatalf("consume held %q", rest)
	}
	if !lt.transcriptActive() {
		t.Fatal("! must open the transcript pager")
	}
	if !lt.tr.showStatus {
		t.Fatal("! must arrive with the figaro status panel up")
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
	for _, b := range []byte{'!', '?', 'Q'} {
		if !opensTranscriptFor(b) {
			t.Fatalf("panel key %q must open the pager", b)
		}
	}
}
