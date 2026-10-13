package cli

import "testing"

func TestInputConsume_BangOpensStatusPanel(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, true)
	if rest := feed(t, in, "!"); len(rest) != 0 {
		t.Fatalf("consume held %q", rest)
	}
	if !lt.transcriptActive() {
		t.Fatal("! must keep the transcript pager up")
	}
	if !lt.tr.showing("status") {
		t.Fatal("! must raise the figaro status panel")
	}
}

// From incipit it opens the pager and acts on arrival.
func TestInputConsume_BangOpensThePagerFromIncipit(t *testing.T) {
	var out countingWriter
	in, lt := navInput(t, &out, false)
	feed(t, in, "!")
	if !lt.transcriptActive() {
		t.Fatal("! no longer opens the pager from incipit")
	}
}

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
