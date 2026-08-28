package cli

// Reading the system clipboard, for `^V` in the command box.
//
// Writing is OSC 52 (term.SetClipboard): the terminal takes a string and the
// program never touches a selection. READING has no equivalent worth using --
// OSC 52 queries are refused by most terminals by default, precisely because a
// program that can read your clipboard can read whatever you last copied
// anywhere. So this shells out, and says so when it cannot.

import (
	"os/exec"
	"strings"
)

// clipboardReaders are tried in order. Wayland first, then X, then macOS: the
// first that EXISTS wins, whether or not it returns anything, because a
// present-but-empty clipboard is an answer and a missing tool is not.
var clipboardReaders = [][]string{
	{"wl-paste", "--no-newline"},
	{"xclip", "-selection", "clipboard", "-o"},
	{"xsel", "--clipboard", "--output"},
	{"pbpaste"},
	{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"},
}

// clipboardRead is the seam. A test that shells out to wl-paste reads the
// DEVELOPER'S clipboard: measured, and it is how a keymap oracle came back
// carrying a directory listing that happened to be on mine. The package's
// tests stub this (see clipboard_stub_test.go); production never touches it.
var clipboardRead = clipboardText

// clipboardText returns the clipboard's contents, or a reason it could not.
func clipboardText() (string, error) {
	var missing []string
	for _, argv := range clipboardReaders {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			missing = append(missing, argv[0])
			continue
		}
		out, err := exec.Command(path, argv[1:]...).Output()
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	return "", errNoClipboardTool{tried: missing}
}

type errNoClipboardTool struct{ tried []string }

func (e errNoClipboardTool) Error() string {
	if len(e.tried) == 0 {
		return "no clipboard tool available"
	}
	return "no clipboard tool: install one of " + strings.Join(e.tried, ", ")
}

// pasteIntoLine folds pasted text into something a ONE-LINE editor can hold.
// Newlines become spaces rather than being dropped: a multi-line paste into a
// command line is nearly always a prompt that wants to arrive whole, and
// silently truncating at the first newline would send half of it.
func pasteIntoLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
