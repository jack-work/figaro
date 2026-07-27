package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jack-work/figaro/internal/term"
)

// Composing a prompt in a terminal editor.
//
// `figaro send --` and the bare `figaro --` with NOTHING after the boundary
// used to be a usage error. They are now an invitation: open a multi-line
// editor and send what gets written. That is what makes a `q` alias expanding
// to `figaro --` usable with no arguments at all.
//
// This is `gum write` without gum. The component gum wraps is
// bubbles/textarea, already a direct dependency here (glamour and huh drag
// bubbletea in besides), so embedding the editor costs no new module and no
// exec of a binary that may not be installed. It also means the editor cannot
// disagree with figaro about terminal state: one process, one raw-mode owner.

// composeCancelled reports that the user dismissed the editor. Callers exit
// quietly rather than treating it as a failure — abandoning a draft is a
// decision, not an error.
type composeCancelled struct{}

func (composeCancelled) Error() string { return "compose cancelled" }

const (
	composeMinRows = 3
	composeMaxRows = 12
)

// composeModel is the editor: a textarea and a one-line hint.
type composeModel struct {
	ta        textarea.Model
	hint      string
	cancelled bool
}

func (m composeModel) Init() tea.Cmd { return textarea.Blink }

func (m composeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A bounded box, not a full-screen takeover: a prompt is usually short,
		// and reserving the whole terminal for one would misstate how much is
		// expected.
		h := msg.Height - 4
		if h > composeMaxRows {
			h = composeMaxRows
		}
		if h < composeMinRows {
			h = composeMinRows
		}
		m.ta.SetWidth(msg.Width - 2)
		m.ta.SetHeight(h)
		return m, nil
	case tea.KeyMsg:
		// Match on String(), not Type. The Type constants are easy to get
		// subtly wrong across bubbletea versions, and a submit key that
		// silently does not fire leaves the user trapped in an editor with no
		// way out but Esc — which throws the draft away. String() is what the
		// library itself prints and is stable.
		switch msg.String() {
		case "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "ctrl+d", "alt+enter":
			// Ctrl-D submits: the same key that ends stdin everywhere else in
			// this CLI, and the same key gum write submits on, so the muscle
			// memory transfers in both directions. Alt-Enter is the fallback
			// for terminals that eat Ctrl-D.
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m composeModel) View() string {
	return "\n" + m.ta.View() + "\n" + term.Dim(m.hint) + "\n"
}

// composePrompt opens the editor and returns what was written.
//
// Returns composeCancelled when the user dismissed it, and when the buffer is
// blank: an empty draft submitted by accident must not open a turn.
func composePrompt(placeholder string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return "", fmt.Errorf("a prompt is required when stdin or stdout is not a terminal " +
			"(pass it after `--`)")
	}
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // a prompt is not a tweet
	ta.Focus()

	// Draw on stderr so a composed prompt can still be piped: `q -- | tee`
	// keeps stdout clean for the reply.
	m := composeModel{ta: ta, hint: "  ctrl-d send · esc cancel"}
	out, err := tea.NewProgram(m, tea.WithOutput(os.Stderr)).Run()
	if err != nil {
		return "", fmt.Errorf("compose: %w", err)
	}
	final, ok := out.(composeModel)
	if !ok || final.cancelled {
		return "", composeCancelled{}
	}
	text := strings.TrimSpace(final.ta.Value())
	if text == "" {
		return "", composeCancelled{}
	}
	return text, nil
}
