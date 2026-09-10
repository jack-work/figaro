package tui

// Non-TTY fallbacks. These reuse the same UX as the pre-huh wizard:
// numbered prompts with `term.ReadPassword` for the passphrase.
// Living here keeps the public API (PromptPassphrase / PickProvider)
// a single function the caller doesn't have to branch around.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

// promptOut and readPassword are variables so a test can drive the
// fallback prompts without a terminal. Production keeps stderr and the
// real no-echo read; nothing else may reassign them.
var (
	promptOut    io.Writer = os.Stderr
	readPassword           = func() ([]byte, error) {
		return term.ReadPassword(int(os.Stdin.Fd()))
	}
	stdinIsTTY = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
)

// promptPassphraseFallback is the same two questions without the huh
// form: a plain numbered prompt for a terminal that cannot host a TUI.
// It splits create from unlock exactly as the TUI does, because a user
// on a dumb terminal is no more able to guess which one he is looking
// at than anyone else.
func promptPassphraseFallback(req PassphraseRequest) ([]byte, error) {
	if !stdinIsTTY() {
		if req.Mode == PassphraseUnlock {
			return nil, fmt.Errorf(
				"unlock %s vault: needs a controlling terminal, but stdin is not a TTY",
				req.app())
		}
		return nil, fmt.Errorf(
			"set up %s secrets vault: needs a controlling terminal, but stdin is not a TTY",
			req.app())
	}
	if req.Mode == PassphraseUnlock {
		return unlockFallback(req)
	}
	return createFallback(req)
}

func createFallback(req PassphraseRequest) ([]byte, error) {
	fmt.Fprintf(promptOut, "\n[%s] First-time setup.\n", req.app())
	fmt.Fprintln(promptOut, "Choose a passphrase to encrypt your credentials at rest.")
	if req.SavesToKeyring {
		fmt.Fprintln(promptOut, "We'll save it to your OS keyring: you won't be asked again.")
	} else {
		fmt.Fprintln(promptOut, "This host has no reachable OS keyring, so you'll be asked on every start.")
		fmt.Fprintln(promptOut, "Run figaro vault init --file to use a key file instead, which never asks.")
	}
	fmt.Fprintln(promptOut)

	for {
		fmt.Fprint(promptOut, "Passphrase: ")
		pp1, err := readPassword()
		fmt.Fprintln(promptOut)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		if len(pp1) == 0 {
			fmt.Fprintln(promptOut, "  passphrase cannot be empty")
			continue
		}
		fmt.Fprint(promptOut, "Confirm:    ")
		pp2, err := readPassword()
		fmt.Fprintln(promptOut)
		if err != nil {
			wipe(pp1)
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		if !bytesEqual(pp1, pp2) {
			wipe(pp1)
			wipe(pp2)
			fmt.Fprintln(promptOut, "  passphrases do not match: try again")
			continue
		}
		wipe(pp2)
		return pp1, nil
	}
}

func unlockFallback(req PassphraseRequest) ([]byte, error) {
	fmt.Fprintf(promptOut, "\n[%s] Unlock the vault.\n", req.app())
	fmt.Fprintln(promptOut, "Enter your passphrase to unlock your stored credentials.")
	fmt.Fprintln(promptOut)

	tries := req.tries()
	for attempt := 1; ; attempt++ {
		fmt.Fprint(promptOut, "Passphrase: ")
		pp, err := readPassword()
		fmt.Fprintln(promptOut)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		if len(pp) == 0 {
			wipe(pp)
			if attempt >= tries {
				return nil, ErrTooManyAttempts
			}
			fmt.Fprintln(promptOut, "  passphrase cannot be empty")
			continue
		}
		if req.Verify == nil {
			return pp, nil
		}
		if err := req.Verify(append([]byte(nil), pp...)); err == nil {
			return pp, nil
		}
		wipe(pp)
		if attempt >= tries {
			return nil, ErrTooManyAttempts
		}
		fmt.Fprintln(promptOut, "  incorrect passphrase: try again")
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
	line, err := term.ReadLine("       Pick [1]: ")
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
