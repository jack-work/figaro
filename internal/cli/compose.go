package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jack-work/figaro/internal/term"
)

// composeCancelled is not a failure: callers exit quietly.
type composeCancelled struct{}

func (composeCancelled) Error() string { return "compose cancelled" }

const (
	composeMinRows = 3
	composeMaxRows = 12
)

type composeModel struct {
	ta        textarea.Model
	hint      string
	cancelled bool
}

func (m composeModel) Init() tea.Cmd { return textarea.Blink }

func (m composeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
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
		// String(), not Type: the Type constants shift across bubbletea
		// versions.
		switch msg.String() {
		case "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "alt+enter", "ctrl+enter", "ctrl+s":
			// Not ^D: the textarea binds it to delete-forward. ^S is
			// for terminals that eat both Enter chords.
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

func composePrompt(placeholder string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return "", fmt.Errorf("a prompt is required when stdin or stdout is not a terminal " +
			"(pass it after `--`)")
	}
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Focus()

	// stderr keeps stdout clean for a piped reply.
	m := composeModel{ta: ta, hint: "  M-enter / ^S send · esc cancel"}
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
