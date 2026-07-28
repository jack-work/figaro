package mouse

import "testing"

func TestParse(t *testing.T) {
	type want struct {
		ok, need bool
		consumed int
		ev       Event
	}
	cases := []struct {
		name string
		in   string
		w    want
	}{
		{
			name: "wheel up",
			in:   "\x1b[<64;10;3M",
			w: want{ok: true, consumed: len("\x1b[<64;10;3M"),
				ev: Event{Button: WheelUp, Base: 64, Mod: 0, X: 10, Y: 3, Pressed: true}},
		},
		{
			name: "wheel down",
			in:   "\x1b[<65;1;1M",
			w: want{ok: true, consumed: len("\x1b[<65;1;1M"),
				ev: Event{Button: WheelDown, Base: 65, Mod: 0, X: 1, Y: 1, Pressed: true}},
		},
		{
			name: "ctrl wheel up",
			in:   "\x1b[<80;5;5M",
			w: want{ok: true, consumed: len("\x1b[<80;5;5M"),
				ev: Event{Button: WheelUp, Base: 64, Mod: 16, X: 5, Y: 5, Pressed: true}},
		},
		{
			name: "release terminator",
			in:   "\x1b[<64;2;2m",
			w: want{ok: true, consumed: len("\x1b[<64;2;2m"),
				ev: Event{Button: WheelUp, Base: 64, Mod: 0, X: 2, Y: 2, Pressed: false}},
		},
		{
			name: "trailing garbage only consumes one event",
			in:   "\x1b[<64;10;3Mqqq",
			w: want{ok: true, consumed: len("\x1b[<64;10;3M"),
				ev: Event{Button: WheelUp, Base: 64, Mod: 0, X: 10, Y: 3, Pressed: true}},
		},
		{name: "split partial", in: "\x1b[<64;10", w: want{need: true}},
		{name: "split at prefix only", in: "\x1b[<", w: want{need: true}},
		{name: "split mid-prefix", in: "\x1b[", w: want{need: true}},
		// A LONE ESC IS A KEYPRESS, NOT A SPLIT MOUSE REPORT. This case used to
		// assert need=true — it pinned the bug rather than the intent. With the
		// pager up, mouse reporting is on and this parser runs first, so claiming
		// the byte parked every bare Escape in the input loop's `pending` buffer
		// until the next keystroke arrived. Esc-to-clear-selection then left its
		// cue painted until the user scrolled.
		{name: "bare esc is not ours", in: "\x1b", w: want{}},
		{name: "non-mouse arrow", in: "\x1b[A", w: want{}},
		{name: "plain char", in: "q", w: want{}},
		{
			name: "shift+alt wheel down",
			in:   "\x1b[<77;7;9M", // 65 + 4 + 8
			w: want{ok: true, consumed: len("\x1b[<77;7;9M"),
				ev: Event{Button: WheelDown, Base: 65, Mod: 12, X: 7, Y: 9, Pressed: true}},
		},
		{
			name: "left press",
			in:   "\x1b[<0;3;4M",
			w: want{ok: true, consumed: len("\x1b[<0;3;4M"),
				ev: Event{Button: Left, Base: 0, Mod: 0, X: 3, Y: 4, Pressed: true}},
		},
		{
			// The RELEASE of the same click. It has to be distinguishable from the
			// press or a click-to-toggle gesture fires twice per click — on the way
			// down and on the way up — and so never appears to do anything.
			name: "left release",
			in:   "\x1b[<0;3;4m",
			w: want{ok: true, consumed: len("\x1b[<0;3;4m"),
				ev: Event{Button: Left, Base: 0, Mod: 0, X: 3, Y: 4, Pressed: false}},
		},
		{
			name: "shift+left press",
			in:   "\x1b[<4;3;4M", // 0 + 4
			w: want{ok: true, consumed: len("\x1b[<4;3;4M"),
				ev: Event{Button: Left, Base: 0, Mod: 4, X: 3, Y: 4, Pressed: true}},
		},
		{
			name: "middle and right are not left",
			in:   "\x1b[<2;9;9M",
			w: want{ok: true, consumed: len("\x1b[<2;9;9M"),
				ev: Event{Button: Right, Base: 2, Mod: 0, X: 9, Y: 9, Pressed: true}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, consumed, ok, need := Parse([]byte(c.in))
			if ok != c.w.ok || need != c.w.need || consumed != c.w.consumed {
				t.Fatalf("got ok=%v need=%v consumed=%d, want ok=%v need=%v consumed=%d",
					ok, need, consumed, c.w.ok, c.w.need, c.w.consumed)
			}
			if ok && ev != c.w.ev {
				t.Fatalf("event mismatch:\n got  %+v\n want %+v", ev, c.w.ev)
			}
		})
	}
}

func TestParseSplitThenComplete(t *testing.T) {
	buf := []byte("\x1b[<64;10")
	_, consumed, ok, need := Parse(buf)
	if ok || !need || consumed != 0 {
		t.Fatalf("first parse: ok=%v need=%v consumed=%d", ok, need, consumed)
	}
	buf = append(buf, []byte(";3M")...)
	ev, consumed, ok, need := Parse(buf)
	if !ok || need || consumed != len(buf) {
		t.Fatalf("second parse: ok=%v need=%v consumed=%d len=%d", ok, need, consumed, len(buf))
	}
	if ev.Button != WheelUp || ev.X != 10 || ev.Y != 3 || !ev.Pressed {
		t.Fatalf("bad event: %+v", ev)
	}
}

func TestControlStrings(t *testing.T) {
	if Enable != "\x1b[?1000h\x1b[?1006h" {
		t.Fatalf("bad Enable")
	}
	if Disable != "\x1b[?1006l\x1b[?1000l" {
		t.Fatalf("bad Disable")
	}
}

// TestModifierAccessors pins the bit meanings against the xterm encoding. They
// are asserted rather than trusted because 4/8/16 is exactly the kind of
// triple a reader "remembers" as 1/2/4 (the CSI-u modifier mask, which IS
// 1/2/4 and is decoded elsewhere in this binary — see navModifiers).
func TestModifierAccessors(t *testing.T) {
	cases := []struct {
		mod                    int
		shift, alt, ctrlWanted bool
	}{
		{0, false, false, false},
		{4, true, false, false},
		{8, false, true, false},
		{16, false, false, true},
		{28, true, true, true},
	}
	for _, c := range cases {
		ev := Event{Mod: c.mod}
		if ev.Shift() != c.shift || ev.Alt() != c.alt || ev.Ctrl() != c.ctrlWanted {
			t.Errorf("Mod=%d: shift=%v alt=%v ctrl=%v, want %v/%v/%v",
				c.mod, ev.Shift(), ev.Alt(), ev.Ctrl(), c.shift, c.alt, c.ctrlWanted)
		}
	}
}
