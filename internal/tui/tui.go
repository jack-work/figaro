// Package tui — figaro's bubbletea-based first-run components.
//
// Built on top of charmbracelet/huh. Two components:
//
//   - PassphrasePrompt: two-field form (passphrase + confirm) that
//     validates match before returning. Used by hush's
//     managed.Options.PromptPassphrase callback.
//
//   - ProviderPicker: a vertically-arranged select with vim (j/k),
//     emacs (ctrl+p/ctrl+n), and arrow-key navigation. Returns the
//     chosen entry.
//
// Both components fall back to a plain numbered prompt when:
//   - stdin/stdout is not a TTY, or
//   - NO_COLOR is set, or
//   - the caller passes Interactive(false) — the figaro config knob.
//
// The fallback path is implemented in firstrun.go's existing
// numbered-menu helpers; the TUI components defer to those when
// detecting non-interactive conditions.
package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/jack-work/figaro/internal/term"
)

// Available reports whether a rich TUI can run in the current
// environment. Callers should branch on this before constructing a
// huh form; for negative cases, fall back to plain prompts.
//
// Conservative: requires a TTY on both stdin and stdout. NO_COLOR is
// honored by lipgloss/huh internally; we still let the form render,
// just without color.
func Available() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	if v, ok := os.LookupEnv("TERM"); ok && (v == "dumb" || v == "") {
		return false
	}
	return true
}

// withFigaroKeymap augments huh's default key bindings with emacs
// equivalents (ctrl+p / ctrl+n) on top of the already-supported vim
// j/k and arrow keys. huh ships j/ctrl+j/ctrl+n/down for next-field
// navigation by default; we add ctrl+p as a synonym for prev.
func withFigaroKeymap(form *huh.Form) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Input.Prev = key.NewBinding(
		key.WithKeys("shift+tab", "ctrl+p"),
		key.WithHelp("ctrl+p", "back"),
	)
	km.Input.Next = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+n"),
		key.WithHelp("enter/ctrl+n", "next"),
	)
	km.Select.Prev = key.NewBinding(
		key.WithKeys("shift+tab", "ctrl+p"),
		key.WithHelp("ctrl+p", "back"),
	)
	km.Select.Next = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+n"),
		key.WithHelp("ctrl+n", "next"),
	)
	km.Select.Up = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	km.Select.Down = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	form.WithKeyMap(km)
	return form
}

// PromptPassphrase shows a two-field form (passphrase + confirm) and
// returns the agreed-upon bytes. Validates: non-empty, both fields
// match. Errors propagate; aborting (esc/ctrl+c) returns
// huh.ErrUserAborted.
//
// The title and description give the user context about what the
// passphrase is for. Pass the consuming app's display name (e.g.
// "figaro") for the appname argument.
func PromptPassphrase(appname string) ([]byte, error) {
	if !Available() {
		return promptPassphraseFallback(appname)
	}

	var pass1, pass2 string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title(fmt.Sprintf("Set up %s secrets vault", appname)).
				Description(
					"Choose a passphrase to encrypt your provider credentials at rest.\n"+
						"We'll save it to your OS keyring — you won't be asked again.\n",
				),
			huh.NewInput().
				Title("Passphrase").
				EchoMode(huh.EchoModePassword).
				Value(&pass1).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("passphrase cannot be empty")
					}
					return nil
				}),
			huh.NewInput().
				Title("Confirm").
				EchoMode(huh.EchoModePassword).
				Value(&pass2).
				Validate(func(s string) error {
					if s != pass1 {
						return fmt.Errorf("does not match")
					}
					return nil
				}),
		),
	)
	withFigaroKeymap(form)
	if err := form.Run(); err != nil {
		return nil, err
	}
	// Wipe the second buffer; return the first.
	out := []byte(pass1)
	zeroString(&pass1)
	zeroString(&pass2)
	return out, nil
}

// ProviderOption is one row in the picker. Label is shown; Hint is
// dimmed alongside. The Key is returned (caller uses it to dispatch
// to the right credential flow).
type ProviderOption struct {
	Key   string // returned to caller
	Label string
	Hint  string
}

// PickProvider shows a select with the given options. First option
// is the default (Enter selects it without moving). Returns the
// chosen Key.
//
// Falls back to a numbered prompt when Available() is false.
func PickProvider(title string, options []ProviderOption) (string, error) {
	if !Available() {
		return pickProviderFallback(title, options)
	}

	if len(options) == 0 {
		return "", fmt.Errorf("no options provided")
	}

	hopts := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		display := o.Label
		if o.Hint != "" {
			display = fmt.Sprintf("%s — %s", o.Label, o.Hint)
		}
		hopts = append(hopts, huh.NewOption(display, o.Key))
	}

	var chosen string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(title).
				Description("Use ↑/↓ or j/k or ctrl+p/ctrl+n to move. Enter to choose.").
				Options(hopts...).
				Value(&chosen),
		),
	)
	withFigaroKeymap(form)
	if err := form.Run(); err != nil {
		return "", err
	}
	return chosen, nil
}

// zeroString tries to overwrite a string's backing bytes. Best-effort
// — Go's string immutability means the runtime may have made copies,
// but doing the wipe here at least kills the local reference and
// shortens the window before GC reclaims.
func zeroString(s *string) {
	// Replace with a same-length zeroed string so the original
	// header points elsewhere immediately.
	if s == nil {
		return
	}
	*s = strings.Repeat("\x00", len(*s))
	*s = ""
}
