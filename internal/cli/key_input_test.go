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

func TestCoalesceNewlineCRLF(t *testing.T) {
	in := &interactiveInput{}
	// Single Enter as raw CR then raw LF (Windows conhost style): the LF
	// following a CR must be swallowed so a toggle binding fires ONCE.
	if in.coalesceNewline(0x0d) {
		t.Fatal("first CR must not be skipped")
	}
	if !in.coalesceNewline(0x0a) {
		t.Fatal("LF paired with prior CR must be skipped")
	}
	// Two Enters in a row: CR CR (Linux) should NOT dedup — both are real.
	in2 := &interactiveInput{}
	if in2.coalesceNewline(0x0d) || in2.coalesceNewline(0x0d) {
		t.Fatal("consecutive CRs are two real presses; neither may be skipped")
	}
	// The mirrored ordering: LF then CR. Second byte skipped.
	in3 := &interactiveInput{}
	if in3.coalesceNewline(0x0a) {
		t.Fatal("first LF must not be skipped")
	}
	if !in3.coalesceNewline(0x0d) {
		t.Fatal("CR paired with prior LF must be skipped")
	}
	// A non-newline byte between two CRs resets state so the second CR still fires.
	in4 := &interactiveInput{}
	if in4.coalesceNewline(0x0d) {
		t.Fatal("first CR must not be skipped")
	}
	if in4.coalesceNewline('x') {
		t.Fatal("normal bytes are never skipped")
	}
	if in4.coalesceNewline(0x0a) {
		t.Fatal("LF after an intervening byte is a fresh press, not the pair")
	}
}

func TestNavKeyFor(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want navKey
	}{
		// CSI — the normal cursor mode.
		{"csi up", "\x1b[A", navUp},
		{"csi down", "\x1b[B", navDown},
		{"csi home", "\x1b[H", navHome},
		{"csi end", "\x1b[F", navEnd},
		// SS3 — application cursor mode (DECCKM), which many terminals switch
		// to the moment a full-screen app takes the alt screen.
		{"ss3 up", "\x1bOA", navUp},
		{"ss3 down", "\x1bOB", navDown},
		{"ss3 home", "\x1bOH", navHome},
		{"ss3 end", "\x1bOF", navEnd},
		// VT220 / rxvt tilde forms.
		{"tilde pageup", "\x1b[5~", navPageUp},
		{"tilde pagedown", "\x1b[6~", navPageDown},
		{"tilde home vt220", "\x1b[1~", navHome},
		{"tilde end vt220", "\x1b[4~", navEnd},
		{"tilde home rxvt", "\x1b[7~", navHome},
		{"tilde end rxvt", "\x1b[8~", navEnd},
		// Modified arrows still name the same key.
		{"ctrl up", "\x1b[1;5A", navUp},
		{"shift pagedown", "\x1b[6;2~", navPageDown},
		// Not navigation: stays swallowed exactly as before.
		{"left", "\x1b[D", navNone},
		{"right", "\x1b[C", navNone},
		{"delete", "\x1b[3~", navNone},
		{"f1 ss3", "\x1bOP", navNone},
		{"f5", "\x1b[15~", navNone},
		{"osc reply", "\x1b]0;title\x07", navNone},
		{"sgr mouse", "\x1b[<64;10;20M", navNone},
		{"dec private", "\x1b[?1u", navNone},
		{"cursor report", "\x1b[24;80R", navNone},
		{"bare esc", "\x1b", navNone},
		{"alt-a", "\x1ba", navNone},
		{"ss3 overlong", "\x1bOAA", navNone},
		{"garbage params", "\x1b[1;2;3A", navNone},
		{"trailing semi", "\x1b[1;A", navNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := navKeyFor([]byte(tc.seq))
			if tc.want == navNone {
				if ok {
					t.Fatalf("navKeyFor(%q) = %+v, want not a navigation key", tc.seq, key)
				}
				return
			}
			if !ok || key.nav != tc.want {
				t.Fatalf("navKeyFor(%q) = %+v, %v; want nav %d", tc.seq, key, ok, tc.want)
			}
			if _, representable := key.asByte(); representable {
				t.Fatal("a navigation key must not masquerade as a character byte")
			}
		})
	}
}

func TestNavKeyModifiers(t *testing.T) {
	key, ok := navKeyFor([]byte("\x1b[1;6A")) // Ctrl+Shift+Up
	if !ok || key.nav != navUp {
		t.Fatalf("navKeyFor = %+v, %v", key, ok)
	}
	if !key.ctrl || !key.shift || key.alt {
		t.Fatalf("modifiers = %+v, want ctrl+shift", key)
	}
}
