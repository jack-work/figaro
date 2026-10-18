package term

import "testing"

func TestOSC52Framing(t *testing.T) {
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
	_ = c.Interactive()
	_, _ = c.Size()
	c.SetClipboard("x")
}

func TestMakeRawNonTTY(t *testing.T) {
	c := NewClient()
	if c.Interactive() {
		t.Skip("stdin is a tty; skipping non-tty error assertion")
	}
	restore, err := c.MakeRaw()
	if err == nil {
		if restore != nil {
			restore()
		}
		return
	}
	if restore != nil {
		t.Errorf("MakeRaw returned err=%v with non-nil restore", err)
	}
}
