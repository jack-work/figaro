//go:build windows

package term

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestRawInputModeEnablesVTAndPreservesUnrelatedFlags(t *testing.T) {
	old := uint32(
		windows.ENABLE_LINE_INPUT |
			windows.ENABLE_ECHO_INPUT |
			windows.ENABLE_PROCESSED_INPUT |
			windows.ENABLE_WINDOW_INPUT |
			windows.ENABLE_MOUSE_INPUT |
			windows.ENABLE_QUICK_EDIT_MODE,
	)

	got := rawInputMode(old)
	for _, flag := range []uint32{
		windows.ENABLE_LINE_INPUT,
		windows.ENABLE_ECHO_INPUT,
		windows.ENABLE_PROCESSED_INPUT,
	} {
		if got&flag != 0 {
			t.Fatalf("raw mode retained input flag %#x: mode=%#x", flag, got)
		}
	}
	if got&windows.ENABLE_VIRTUAL_TERMINAL_INPUT == 0 {
		t.Fatalf("raw mode did not enable VT input: mode=%#x", got)
	}
	for _, flag := range []uint32{
		windows.ENABLE_WINDOW_INPUT,
		windows.ENABLE_MOUSE_INPUT,
		windows.ENABLE_QUICK_EDIT_MODE,
	} {
		if got&flag == 0 {
			t.Fatalf("raw mode cleared unrelated flag %#x: mode=%#x", flag, got)
		}
	}
}

// The OUTPUT half. A bare conhost hands us 0x0003 — no VT processing — and
// there \x1b[?1049h is inert: the pager paints into the PRIMARY buffer and the
// transcript is left in the shell's scrollback.
func TestVTOutputModeArmsVTAndPreservesUnrelatedFlags(t *testing.T) {
	const conhostDefault = uint32(windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_WRAP_AT_EOL_OUTPUT)

	got := vtOutputMode(conhostDefault)
	if got&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING == 0 {
		t.Fatalf("output mode did not enable VT processing: mode=%#x", got)
	}
	if got&conhostDefault != conhostDefault {
		t.Fatalf("output mode cleared a flag the console arrived with: %#x -> %#x", conhostDefault, got)
	}
	if vtOutputMode(got) != got {
		t.Fatalf("arming an armed console is not idempotent: %#x -> %#x", got, vtOutputMode(got))
	}
}

// CANARY. DISABLE_NEWLINE_AUTO_RETURN must NOT ride along with the startup
// arming: it stops a bare LF from implying a carriage return, so every
// fmt.Println in `list`/`show`/`status` staircases rightward until it wraps —
// which is indistinguishable, to the eye, from figaro ignoring the terminal
// width. It belongs to the painter alone (ArmDeferredWrap), because only the
// painter writes explicit \r and does its own cursor math.
//
// Put the flag back into vtOutputMode and this test fails.
func TestStartupArmingDoesNotDisableNewlineAutoReturn(t *testing.T) {
	const conhostDefault = uint32(windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_WRAP_AT_EOL_OUTPUT)

	if got := vtOutputMode(conhostDefault); got&windows.DISABLE_NEWLINE_AUTO_RETURN != 0 {
		t.Fatalf("startup arming disabled newline auto-return: mode=%#x", got)
	}
	// A console that ALREADY has the flag keeps it: arming adds, never clears.
	armed := conhostDefault | windows.DISABLE_NEWLINE_AUTO_RETURN
	if got := vtOutputMode(armed); got&windows.DISABLE_NEWLINE_AUTO_RETURN == 0 {
		t.Fatalf("arming cleared a flag the console arrived with: %#x -> %#x", armed, got)
	}
}
