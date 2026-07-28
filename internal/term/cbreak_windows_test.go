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
