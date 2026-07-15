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
		{name: "split just esc", in: "\x1b", w: want{need: true}},
		{name: "non-mouse arrow", in: "\x1b[A", w: want{}},
		{name: "plain char", in: "q", w: want{}},
		{
			name: "shift+alt wheel down",
			in:   "\x1b[<77;7;9M", // 65 + 4 + 8
			w: want{ok: true, consumed: len("\x1b[<77;7;9M"),
				ev: Event{Button: WheelDown, Base: 65, Mod: 12, X: 7, Y: 9, Pressed: true}},
		},
		{
			name: "other button",
			in:   "\x1b[<0;3;4M",
			w: want{ok: true, consumed: len("\x1b[<0;3;4M"),
				ev: Event{Button: Other, Base: 0, Mod: 0, X: 3, Y: 4, Pressed: true}},
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
