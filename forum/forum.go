package forum

import (
	"context"
	"figaro/figaro"
	"figaro/logging"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"go.opentelemetry.io/otel/trace"
)

type model struct {
	choices  []string         // items on the to-do list
	cursor   int              // which to-do list item our cursor is pointing at
	selected map[int]struct{} // which to-do items are selected
}

type Forum struct {
	program *tea.Program
}

func GetMarkdownString(message string) (*string, error) {
	out, err := glamour.Render(message, "dark")
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func OpenForum(ctx context.Context, tp trace.TracerProvider, update chan figaro.Event) {
	ctx, cancel := context.WithCancelCause(ctx)

	tracer := tp.Tracer("forum")
	ctx, span := tracer.Start(ctx, "request")
	defer span.End()
	// p := tea.NewProgram(initialModel())
	var sb strings.Builder
	go func() {
		defer cancel(ctx.Err())
	loop:
		for {
			select {
			case event := <-update:
				switch event.Type {
				case figaro.TaskStarted:
				case figaro.TaskCompleted:
				case figaro.TaskFailed:
				case figaro.MessageStarted:
					fmt.Print(event.Data)
					sb.WriteString(event.Data)
				case figaro.MessagePart:
					fmt.Print(event.Data)
					sb.WriteString(event.Data)
					fmt.Print(event.Data)
				case figaro.MessageEnded:
					s, err := GetMarkdownString(sb.String())
					if err != nil {
						logging.EzFail(span, err)
						break loop
					}
					fmt.Println(*s)
				default:
				}
			// I'm pretty sure this doesn't fire because the program ends before it can be read.
			// To get this to work, we probably need to defer this bit to make sure it runs,
			// rather that programming it to a loop.
			case <-ctx.Done():
				fmt.Println("\n\nDone!")
				sb.WriteString("___")
				break loop
			}
		}
	}()
}

func initialModel() model {
	return model{
		// Our to-do list is a grocery list
		choices: []string{"Buy carrots", "Buy celery", "Buy kohlrabi"},

		// A map which indicates which choices are selected. We're using
		// the  map like a mathematical set. The keys refer to the indexes
		// of the `choices` slice, above.
		selected: make(map[int]struct{}),
	}
}

func (m model) Init() tea.Cmd {
	// Just return `nil`, which means "no I/O right now, please."
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// Is it a key press?
	case tea.KeyMsg:

		// Cool, what was the actual key pressed?
		switch msg.String() {

		// These keys should exit the program.
		case "ctrl+c", "q":
			return m, tea.Quit

		// The "up" and "k" keys move the cursor up
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		// The "down" and "j" keys move the cursor down
		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}

		// The "enter" key and the spacebar (a literal space) toggle
		// the selected state for the item that the cursor is pointing at.
		case "enter", " ":
			_, ok := m.selected[m.cursor]
			if ok {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
			}
		}
	}

	// Return the updated model to the Bubble Tea runtime for processing.
	// Note that we're not returning a command.
	return m, nil
}

func (m model) View() string {
	// The header
	s := "What should we buy at the market?\n\n"

	// Iterate over our choices
	for i, choice := range m.choices {

		// Is the cursor pointing at this choice?
		cursor := " " // no cursor
		if m.cursor == i {
			cursor = ">" // cursor!
		}

		// Is this choice selected?
		checked := " " // not selected
		if _, ok := m.selected[i]; ok {
			checked = "x" // selected!
		}

		// Render the row
		s += fmt.Sprintf("%s [%s] %s\n", cursor, checked, choice)
	}

	// The footer
	s += "\nPress q to quit.\n"

	// Send the UI for rendering
	return s
}
