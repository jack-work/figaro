package tui

// Non-TTY fallbacks. These reuse the same UX as the pre-huh wizard:
// numbered prompts with `term.ReadPassword` for the passphrase.
// Living here keeps the public API (PromptPassphrase / PickProvider)
// a single function the caller doesn't have to branch around.

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

func promptPassphraseFallback(appname string) ([]byte, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf(
			"set up %s secrets vault: needs a controlling terminal, but stdin is not a TTY",
			appname)
	}
	fmt.Fprintf(os.Stderr, "\n[%s] First-time setup.\n", appname)
	fmt.Fprintln(os.Stderr, "Choose a passphrase to encrypt your credentials at rest.")
	fmt.Fprintln(os.Stderr, "We'll save it to your OS keyring — you won't be asked again.")
	fmt.Fprintln(os.Stderr)

	for {
		fmt.Fprint(os.Stderr, "Passphrase: ")
		pp1, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		if len(pp1) == 0 {
			fmt.Fprintln(os.Stderr, "  passphrase cannot be empty")
			continue
		}
		fmt.Fprint(os.Stderr, "Confirm:    ")
		pp2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			wipe(pp1)
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		if !bytesEqual(pp1, pp2) {
			wipe(pp1)
			wipe(pp2)
			fmt.Fprintln(os.Stderr, "  passphrases do not match — try again")
			continue
		}
		wipe(pp2)
		return pp1, nil
	}
}

func pickProviderFallback(title string, options []ProviderOption) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options provided")
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  "+title)
	fmt.Fprintln(os.Stderr)
	for i, o := range options {
		num := fmt.Sprintf("[%d]", i+1)
		hint := ""
		if o.Hint != "" {
			hint = "   " + o.Hint
		}
		fmt.Fprintf(os.Stderr, "       %s  %s%s\n", num, o.Label, hint)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "       Pick [1]: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read choice: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return options[0].Key, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(options) {
		return "", fmt.Errorf("invalid choice %q (pick 1-%d)", line, len(options))
	}
	return options[n-1].Key, nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
