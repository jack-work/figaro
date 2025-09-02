package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type model struct {
	conversation *Conversation
	selected     int
	messages     []string
	fullMessages []string
	expanded     map[int]bool
	viewport     viewport.Model
	renderer     *glamour.TermRenderer
}

var (
	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("240")).
			Foreground(lipgloss.Color("255")).
			Width(100)

	normalStyle = lipgloss.NewStyle().
			Width(100)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("6"))

	metaStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")).
			Width(100)
)

func initialModel(conv *Conversation) model {
	bgColor := "#000000"
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(ansi.StyleConfig{
			Document: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					BackgroundColor: &bgColor, // Set your background here
				},
			},
			Paragraph: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					BackgroundColor: &bgColor, // Ensure paragraphs also get background
				},
			},
			// Add other elements as needed
			Heading: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					BackgroundColor: &bgColor,
				},
			},
		}),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create renderer: %v", err))
	}

	messages := make([]string, len(conv.Messages))
	fullMessages := make([]string, len(conv.Messages))

	for i, msg := range conv.Messages {
		roleIcon := "👤"
		if msg.Role == "assistant" {
			roleIcon = "🤖"
		}

		header := fmt.Sprintf("Message #%d | %s %s", i+1, roleIcon, cases.Title(language.English).String(msg.Role))
		meta := fmt.Sprintf("Time: %s | Hash: %s",
			msg.Timestamp.Format("2006-01-02 15:04:05"),
			msg.Hash[:8])

		if msg.PrevHash != "" {
			meta += fmt.Sprintf(" | Previous: %s", msg.PrevHash[:8])
		}

		fullContent := strings.TrimSpace(msg.Content)
		shortContent := fullContent
		if len(shortContent) > 200 {
			shortContent = shortContent[:200] + "..."
		}

		messages[i] = fmt.Sprintf("%s\n%s\n\n%s", header, meta, shortContent)
		fullMessages[i] = fmt.Sprintf("%s\n%s\n\n%s", header, meta, fullContent)
	}

	vp := viewport.New(80, 20)

	m := model{
		conversation: conv,
		selected:     len(messages) - 1,
		messages:     messages,
		fullMessages: fullMessages,
		expanded:     make(map[int]bool),
		viewport:     vp,
		renderer:     renderer,
	}

	m.updateContent()
	return m
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 4
		m.updateStyles(msg.Width)
		m.updateContent()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				m.updateContent()
				m.ensureSelectedVisible()
			}
			return m, nil
		case "down", "j":
			if m.selected < len(m.messages)-1 {
				m.selected++
				m.updateContent()
				m.ensureSelectedVisible()
			}
			return m, nil
		case "enter":
			m.expanded[m.selected] = !m.expanded[m.selected]
			m.updateContent()
			m.ensureSelectedVisible()
			return m, nil
		}
	}

	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *model) updateStyles(width int) {
	selectedStyle = selectedStyle.Width(width)
	normalStyle = normalStyle.Width(width)
	separatorStyle = separatorStyle.Width(width)
}

func (m *model) updateContent() {
	if len(m.messages) == 0 {
		return
	}

	var content strings.Builder
	for i := range m.messages {
		msg := m.conversation.Messages[i]

		// Create header and meta info
		roleIcon := "👤"
		if msg.Role == "assistant" {
			roleIcon = "🤖"
		}

		header := fmt.Sprintf("## %s %s #%d", roleIcon, cases.Title(language.English).String(msg.Role), i+1)
		meta := fmt.Sprintf("**Time:** %s | **Hash:** `%s`",
			msg.Timestamp.Format("2006-01-02 15:04:05"),
			msg.Hash[:8])

		if msg.PrevHash != "" {
			meta += fmt.Sprintf(" | **Previous:** `%s`", msg.PrevHash[:8])
		}

		// Get message content (full or truncated)
		messageContent := strings.TrimSpace(msg.Content)
		if !m.expanded[i] && len(messageContent) > 200 {
			messageContent = messageContent[:200] + "..."
		}

		// Render markdown content
		renderedContent, err := m.renderer.Render(messageContent)
		if err != nil {
			renderedContent = messageContent
		}

		// Render header/meta as markdown
		headerMetaText := fmt.Sprintf("%s\n%s", header, meta)
		renderedHeader, err := m.renderer.Render(headerMetaText)
		if err != nil {
			renderedHeader = headerMetaText
		}

		// Apply lipgloss styling to the rendered markdown
		if i == m.selected {
			content.WriteString(selectedStyle.Render(renderedHeader))
			content.WriteString("\n")
			content.WriteString(selectedStyle.Render(renderedContent))
		} else {
			content.WriteString(normalStyle.Render(renderedHeader))
			content.WriteString("\n")
			content.WriteString(normalStyle.Render(renderedContent))
		}

		// Add separator between messages
		if i < len(m.messages)-1 {
			separator := strings.Repeat("─", 80)
			content.WriteString("\n")
			content.WriteString(separatorStyle.Render(separator))
			content.WriteString("\n")
		}
	}

	m.viewport.SetContent(content.String())
}

func (m *model) ensureSelectedVisible() {
	if len(m.messages) == 0 {
		return
	}

	// Get the content of the selected message
	selectedMsg := m.messages[m.selected]
	if m.expanded[m.selected] {
		selectedMsg = m.fullMessages[m.selected]
	}

	// Render the selected message to get its actual displayed height
	renderedMsg, err := m.renderer.Render(selectedMsg)
	if err != nil {
		renderedMsg = selectedMsg
	}

	// Calculate the position and height of the selected message
	linePos := 0
	for i := 0; i < m.selected; i++ {
		msg := m.messages[i]
		if m.expanded[i] {
			msg = m.fullMessages[i]
		}

		rendered, err := m.renderer.Render(msg)
		if err != nil {
			rendered = msg
		}

		linePos += strings.Count(rendered, "\n") + 1 // +1 for the message itself, no spacing
	}

	// Calculate the height of the selected message
	selectedHeight := strings.Count(renderedMsg, "\n") + 1

	currentTop := m.viewport.YOffset
	viewportHeight := m.viewport.Height

	// Check if the selected message is fully visible
	messageTop := linePos
	messageBottom := linePos + selectedHeight

	// Only scroll if the message is not fully visible
	if messageTop < currentTop {
		// Message starts above viewport, scroll to show the top
		m.viewport.SetYOffset(messageTop)
	} else if messageBottom > currentTop+viewportHeight {
		// Message extends below viewport
		if selectedHeight <= viewportHeight {
			// If message fits in viewport, show it completely
			m.viewport.SetYOffset(messageBottom - viewportHeight)
		} else {
			// If message is larger than viewport, show from the top
			m.viewport.SetYOffset(messageTop)
		}
	}
}

func (m model) executeAction() tea.Cmd {
	return tea.Sequence(
		tea.Printf("Action executed on message #%d", m.selected+1),
		tea.Quit,
	)
}

func (m model) View() string {
	if len(m.messages) == 0 {
		return "No messages in conversation.\n\nPress 'q' to quit."
	}

	var b strings.Builder

	title := fmt.Sprintf("Conversation: %s", m.conversation.Name)
	if m.conversation.Parent != nil {
		title += fmt.Sprintf(" (forked from %s)", *m.conversation.Parent)
	}

	b.WriteString(headerStyle.Render(title))
	b.WriteString("\n")

	scrollPercent := int(m.viewport.ScrollPercent() * 100)
	expandedInfo := ""
	if m.expanded[m.selected] {
		expandedInfo = " | EXPANDED"
	}
	b.WriteString(metaStyle.Render(fmt.Sprintf("Messages: %d | Scroll: %d%%%s | Use ↑↓/jk to navigate, Enter to expand/collapse, q to quit", len(m.messages), scrollPercent, expandedInfo)))
	b.WriteString("\n\n")

	b.WriteString(m.viewport.View())

	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func runInteractiveView(conversationName string) error {
	conv, err := loadConversation(conversationName)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}

	if len(conv.Messages) == 0 {
		fmt.Printf("# Conversation: %s\n\n*No messages yet*\n", conv.Name)
		return nil
	}

	p := tea.NewProgram(initialModel(conv))
	_, err = p.Run()
	return err
}
