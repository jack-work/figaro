// Package tui holds the huh-based first-run prompts.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/jack-work/figaro/internal/term"
)

// Available reports whether a huh form can run here.
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

// withFigaroKeymap adds ctrl+p/ctrl+n beside huh's j/k and arrows.
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

type PassphraseMode int

const (
	// PassphraseCreate asks twice: a typo is unrecoverable.
	PassphraseCreate PassphraseMode = iota
	PassphraseUnlock
)

type PassphraseRequest struct {
	App  string
	Mode PassphraseMode
	// SavesToKeyring, in create mode, is the only case that may promise
	// "you won't be asked again".
	SavesToKeyring bool
	// Verify, in unlock mode, enables the retry loop; nil means one attempt
	// and the caller checks.
	Verify func(pp []byte) error
	// Tries bounds unlock attempts; zero means defaultPassphraseTries.
	Tries int
}

const defaultPassphraseTries = 3

func (r PassphraseRequest) tries() int {
	if r.Mode != PassphraseUnlock || r.Verify == nil {
		return 1
	}
	if r.Tries > 0 {
		return r.Tries
	}
	return defaultPassphraseTries
}

func (r PassphraseRequest) app() string {
	if r.App == "" {
		return "figaro"
	}
	return r.App
}

// ErrTooManyAttempts deliberately wraps no crypto error.
var ErrTooManyAttempts = errors.New("incorrect passphrase")

// PromptPassphrase returns huh.ErrUserAborted on esc/ctrl+c.
func PromptPassphrase(req PassphraseRequest) ([]byte, error) {
	if !Available() {
		return promptPassphraseFallback(req)
	}
	if req.Mode == PassphraseCreate {
		return promptCreate(req)
	}
	return promptUnlock(req)
}

func promptCreate(req PassphraseRequest) ([]byte, error) {
	var pass1, pass2 string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title(fmt.Sprintf("Set up %s secrets vault", req.app())).
				Description(createNote(req)),
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
	out := []byte(pass1)
	zeroString(&pass1)
	zeroString(&pass2)
	return out, nil
}

func createNote(req PassphraseRequest) string {
	note := "Choose a passphrase to encrypt your provider credentials at rest.\n"
	if req.SavesToKeyring {
		return note + "We'll save it to your OS keyring: you won't be asked again.\n"
	}
	return note +
		"This host has no reachable OS keyring, so you'll be asked for it\n" +
		"on every start. Escape now and run figaro vault init --file to use\n" +
		"a key file instead, which never asks.\n"
}

func promptUnlock(req PassphraseRequest) ([]byte, error) {
	tries := req.tries()
	for attempt := 1; ; attempt++ {
		var pass string
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewNote().
					Title(fmt.Sprintf("Unlock %s vault", req.app())).
					Description(unlockNote(attempt, tries)),
				huh.NewInput().
					Title("Passphrase").
					EchoMode(huh.EchoModePassword).
					Value(&pass).
					Validate(func(s string) error {
						if s == "" {
							return fmt.Errorf("passphrase cannot be empty")
						}
						return nil
					}),
			),
		)
		withFigaroKeymap(form)
		if err := form.Run(); err != nil {
			return nil, err
		}
		out := []byte(pass)
		zeroString(&pass)
		if req.Verify == nil {
			return out, nil
		}
		if err := req.Verify(append([]byte(nil), out...)); err == nil {
			return out, nil
		}
		wipe(out)
		if attempt >= tries {
			return nil, ErrTooManyAttempts
		}
	}
}

func unlockNote(attempt, tries int) string {
	if attempt == 1 {
		return "Enter your passphrase to unlock your stored credentials.\n"
	}
	left := tries - attempt + 1
	word := "tries"
	if left == 1 {
		word = "try"
	}
	return fmt.Sprintf("incorrect passphrase: %d %s left.\n", left, word)
}

type ProviderOption struct {
	Key   string
	Label string
	Hint  string
}

// PickProvider returns the chosen Key; the first option is the default.
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
			display = fmt.Sprintf("%s: %s", o.Label, o.Hint)
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

// zeroString is best-effort: the runtime may hold copies.
func zeroString(s *string) {
	if s == nil {
		return
	}
	*s = strings.Repeat("\x00", len(*s))
	*s = ""
}
