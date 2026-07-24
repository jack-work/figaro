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

func TestConsumeEscapeSequence(t *testing.T) {
	cases := []struct {
		name     string
		in       []byte
		consumed int
		need     bool
	}{
		{"bare esc alone", []byte{0x1b}, 0, false},
		{"csi up arrow", []byte("\x1b[A"), 3, false},
		{"csi params final", []byte("\x1b[1;2H"), 6, false},
		{"ss3 f-key", []byte("\x1bOP"), 3, false},
		{"ss3 needs more", []byte("\x1bO"), 0, true},
		{"csi incomplete", []byte("\x1b[1;5"), 0, true},
		{"osc bel-terminated", []byte("\x1b]0;title\x07"), 10, false},
		{"osc st-terminated", []byte("\x1b]0;t\x1b\\"), 7, false},
		{"alt-only prefix stays bare", []byte{0x1b, 'a'}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			consumed, need := consumeEscapeSequence(tc.in)
			if consumed != tc.consumed || need != tc.need {
				t.Fatalf("consumeEscapeSequence(%q) = %d,%v; want %d,%v",
					tc.in, consumed, need, tc.consumed, tc.need)
			}
		})
	}
}
