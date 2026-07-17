package cli

import "testing"

func TestParseModifiedKeyCSIuRangeSelection(t *testing.T) {
	key, consumed, ok, need := parseModifiedKey([]byte("\x1b[110;6u"))
	if !ok || need || consumed != len("\x1b[110;6u") {
		t.Fatalf("CSI-u parse = %+v, %d, %v, %v", key, consumed, ok, need)
	}
	if key.code != 'n' || !key.ctrl || !key.shift || key.alt {
		t.Fatalf("CSI-u modifiers = %+v", key)
	}
}

func TestParseModifiedKeyAltCtrlFallback(t *testing.T) {
	key, consumed, ok, need := parseModifiedKey([]byte{0x1b, 0x10})
	if !ok || need || consumed != 2 {
		t.Fatalf("Alt-Ctrl parse = %+v, %d, %v, %v", key, consumed, ok, need)
	}
	if key.code != 'p' || !key.ctrl || !key.alt || key.shift {
		t.Fatalf("Alt-Ctrl modifiers = %+v", key)
	}
}

func TestOpensTranscriptForOutputHotkeys(t *testing.T) {
	for _, key := range []byte{'j', 'k', '/', '?', 0x0e, 0x10, 0x0d} {
		if !opensTranscriptFor(key) {
			t.Fatalf("key %q must enter transcript", key)
		}
	}
	if opensTranscriptFor('y') {
		t.Fatal("copying the aria id must stay available in incipit")
	}
}
