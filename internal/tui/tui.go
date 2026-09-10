// Package tui: figaro's bubbletea-based first-run components.
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

// Available reports whether a rich TUI can run in the current
// environment. Callers should branch on this before constructing a
// huh form; for negative cases, fall back to plain prompts.
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

// PassphraseMode says which of the two questions we are asking. They
// are different questions and they used to share one screen, which is
// how a returning user was told to "choose a passphrase" and then had
// his correct answer rejected: the confirm field only checked that he
// had typed the same new passphrase twice.
type PassphraseMode int

const (
	// PassphraseCreate: there is no identity yet. Ask twice, since a
	// typo here is unrecoverable.
	PassphraseCreate PassphraseMode = iota
	// PassphraseUnlock: an identity exists. Ask once and check the
	// answer against it.
	PassphraseUnlock
)

// PassphraseRequest is what the caller knows and the prompt cannot
// work out for itself.
type PassphraseRequest struct {
	// App names the vault in the prompt ("figaro").
	App string
	// Mode picks the screen.
	Mode PassphraseMode
	// SavesToKeyring is honored in create mode: promise silence only
	// when there is a keyring to be silent with. On a host where no
	// Secret Service can be reached, the old screen's "you won't be
	// asked again" was a lie told once per command.
	SavesToKeyring bool
	// Verify, in unlock mode, reports whether the typed passphrase
	// decrypts the identity. Non-nil enables the retry loop; nil means
	// the caller checks for itself and one attempt is all we make.
	Verify func(pp []byte) error
	// Tries bounds unlock attempts. Zero means defaultPassphraseTries.
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

// ErrTooManyAttempts ends the unlock loop. It carries no wrapped
// crypto error: "incorrect passphrase" is the whole of what the user
// needs, and a wrapped age failure underneath it reads as a crash.
var ErrTooManyAttempts = errors.New("incorrect passphrase")

// PromptPassphrase asks for the passphrase that encrypts the vault.
// In create mode it asks twice and returns the agreed bytes; in unlock
// mode it asks once per attempt and returns the first answer that
// verifies. Aborting (esc/ctrl+c) returns huh.ErrUserAborted.
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
	// Wipe the second buffer; return the first.
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
	// No keyring here, so say what actually happens, and name the way out.
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

// zeroString tries to overwrite a string's backing bytes. Best-effort
// : Go's string immutability means the runtime may have made copies,
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
