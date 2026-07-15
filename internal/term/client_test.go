package term

import "testing"

func TestOSC52Framing(t *testing.T) {
	// "hi" -> base64 "aGk="
	got := OSC52("hi")
	want := "\x1b]52;c;aGk=\x07"
	if got != want {
		t.Errorf("OSC52(\"hi\") = %q, want %q", got, want)
	}
}

func TestOSC52Empty(t *testing.T) {
	got := OSC52("")
	want := "\x1b]52;c;\x07"
	if got != want {
		t.Errorf("OSC52(\"\") = %q, want %q", got, want)
	}
}

func TestOSC52Unicode(t *testing.T) {
	// "é" (U+00E9) -> UTF-8 0xC3 0xA9 -> base64 "w6k="
	got := OSC52("é")
	want := "\x1b]52;c;w6k=\x07"
	if got != want {
		t.Errorf("OSC52(\"é\") = %q, want %q", got, want)
	}
}

func TestNewClientNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewClient panicked: %v", r)
		}
	}()
	c := NewClient()
	_ = c.IsTTY()
	_, _ = c.Size()
	// OSC52 write via SetClipboard should not panic.
	c.SetClipboard("x")
}

func TestMakeRawNonTTY(t *testing.T) {
	c := NewClient()
	if c.IsTTY() {
		t.Skip("stdin is a tty; skipping non-tty error assertion")
	}
	restore, err := c.MakeRaw()
	if err == nil {
		// Some sandboxes give stdin a pty-ish fd; tolerate but restore.
		if restore != nil {
			restore()
		}
		return
	}
	if restore != nil {
		t.Errorf("MakeRaw returned err=%v with non-nil restore", err)
	}
}
