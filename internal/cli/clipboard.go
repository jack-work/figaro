package cli

// Reading is a shell-out: most terminals refuse OSC 52 queries by default.

import (
	"os/exec"
	"strings"
)

// The first tool that exists wins: a present-but-empty clipboard is an answer.
var clipboardReaders = [][]string{
	{"wl-paste", "--no-newline"},
	{"xclip", "-selection", "clipboard", "-o"},
	{"xsel", "--clipboard", "--output"},
	{"pbpaste"},
	{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"},
}

// Tests stub this: a real shell-out reads the developer's clipboard.
var clipboardRead = clipboardText

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

// Newlines become spaces: truncating at the first would send half a prompt.
func pasteIntoLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
