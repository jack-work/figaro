package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type model struct {
	conversation     *Conversation
	selected         int
	messages         []string
	fullMessages     []string
	expanded         map[int]bool
	viewport         viewport.Model
	renderer         *glamour.TermRenderer
	selectedRenderer *glamour.TermRenderer
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

func createRenderer(backgroundColor *string) (*glamour.TermRenderer, error) {

	var baseStyle ansi.StyleConfig
	if termenv.HasDarkBackground() {
		baseStyle = styles.DarkStyleConfig
	} else {
		baseStyle = styles.LightStyleConfig
	}
	if backgroundColor != nil {
		baseStyle.Document.BackgroundColor = backgroundColor
		baseStyle.Paragraph.BackgroundColor = backgroundColor
		baseStyle.CodeBlock.BackgroundColor = backgroundColor
		baseStyle.Text.BackgroundColor = backgroundColor
	}
	// 1. Set Margin to 0 for all block elements
	var marginStyle uint = 0
	baseStyle.Document.Margin = &marginStyle
	baseStyle.Paragraph.Margin = &marginStyle
	baseStyle.BlockQuote.Margin = &marginStyle
	baseStyle.CodeBlock.StyleBlock.Margin = &marginStyle
	baseStyle.Heading.Margin = &marginStyle
	baseStyle.H1.Margin = &marginStyle
	baseStyle.H2.Margin = &marginStyle
	baseStyle.H3.Margin = &marginStyle
	baseStyle.H4.Margin = &marginStyle
	baseStyle.H5.Margin = &marginStyle
	baseStyle.H6.Margin = &marginStyle

	// 2. Remove BlockPrefix and BlockSuffix newlines
	baseStyle.Document.StylePrimitive.BlockPrefix = ""
	baseStyle.Document.StylePrimitive.BlockSuffix = ""
	baseStyle.Paragraph.StylePrimitive.BlockPrefix = ""
	baseStyle.Paragraph.StylePrimitive.BlockSuffix = ""

	// 3. Remove BlockSuffix from headings (they have "\n" by default)
	baseStyle.Heading.StylePrimitive.BlockSuffix = ""
	baseStyle.H1.StylePrimitive.BlockPrefix = ""
	baseStyle.H1.StylePrimitive.BlockSuffix = ""
	baseStyle.H2.StylePrimitive.BlockSuffix = ""
	baseStyle.H3.StylePrimitive.BlockSuffix = ""
	baseStyle.H4.StylePrimitive.BlockSuffix = ""
	baseStyle.H5.StylePrimitive.BlockSuffix = ""
	baseStyle.H6.StylePrimitive.BlockSuffix = ""

	return glamour.NewTermRenderer(
		glamour.WithStyles(baseStyle),
		glamour.WithWordWrap(80),
	)
}

func initialModel(conv *Conversation) model {
	// Create normal renderer (no background override)
	renderer, err := createRenderer(nil)
	if err != nil {
		panic(fmt.Sprintf("failed to create renderer: %v", err))
	}

	// Create selected renderer with background color matching selection
	selectedBgColor := "#585858" // Color 240 in hex
	selectedRenderer, err := createRenderer(&selectedBgColor)
	if err != nil {
		panic(fmt.Sprintf("failed to create selected renderer: %v", err))
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
		conversation:     conv,
		selected:         len(messages) - 1,
		messages:         messages,
		fullMessages:     fullMessages,
		expanded:         make(map[int]bool),
		viewport:         vp,
		renderer:         renderer,
		selectedRenderer: selectedRenderer,
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
		case "u":
			m.viewport.ScrollDown(1)
			return m, nil
		case "i":
			m.viewport.ScrollUp(1)
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
		meta := fmt.Sprintf("### Time: %s | Hash: `%s`",
			msg.Timestamp.Format("2006-01-02 15:04:05"),
			msg.Hash[:8])

		if msg.PrevHash != "" {
			meta += fmt.Sprintf(" | Previous: `%s`", msg.PrevHash[:8])
		}

		// Get message content (full or truncated)
		messageContent := strings.TrimSpace(msg.Content)
		if !m.expanded[i] && len(messageContent) > 200 {
			messageContent = messageContent[:200] + "..."
		}

		// Render header/meta as markdown
		headerMetaText := fmt.Sprintf("%s\n%s", header, meta)

		// Use appropriate renderer based on selection
		var renderedHeader, renderedContent string
		var err error

		if i == m.selected {
			renderedHeader, err = m.selectedRenderer.Render(headerMetaText)
			if err != nil {
				renderedHeader = headerMetaText
			}
			renderedContent, err = m.selectedRenderer.Render(messageContent)
			if err != nil {
				renderedContent = messageContent
			}
		} else {
			renderedHeader, err = m.renderer.Render(headerMetaText)
			if err != nil {
				renderedHeader = headerMetaText
			}
			renderedContent, err = m.renderer.Render(messageContent)
			if err != nil {
				renderedContent = messageContent
			}
		}

		content.WriteString(renderedHeader)
		content.WriteString("\n")
		content.WriteString(renderedContent)

		// Add separator between messages
		if i < len(m.messages)-1 {
			separator := strings.Repeat("─", 80)
			content.WriteString("\n")
			content.WriteString(separatorStyle.Render(separator))
		}
	}

	m.viewport.SetContent(content.String())
}

func (m *model) ensureSelectedVisible() {
	if len(m.messages) == 0 {
		return
	}

	// Calculate the line position of the selected message by counting rendered lines
	linePos := 0
	for i := 0; i < m.selected; i++ {
		msg := m.conversation.Messages[i]

		// Create header and meta for this message
		roleIcon := "👤"
		if msg.Role == "assistant" {
			roleIcon = "🤖"
		}
		header := fmt.Sprintf("## %s %s #%d", roleIcon, cases.Title(language.English).String(msg.Role), i+1)
		meta := fmt.Sprintf("### Time: %s | Hash: `%s`",
			msg.Timestamp.Format("2006-01-02 15:04:05"),
			msg.Hash[:8])
		if msg.PrevHash != "" {
			meta += fmt.Sprintf(" | Previous: `%s`", msg.PrevHash[:8])
		}

		headerMetaText := fmt.Sprintf("%s\n%s", header, meta)

		// Get message content
		messageContent := strings.TrimSpace(msg.Content)
		if !m.expanded[i] && len(messageContent) > 200 {
			messageContent = messageContent[:200] + "..."
		}

		// Render both parts to get accurate line count
		var renderedHeader, renderedContent string
		var err error

		renderedHeader, err = m.renderer.Render(headerMetaText)
		if err != nil {
			renderedHeader = headerMetaText
		}
		renderedContent, err = m.renderer.Render(messageContent)
		if err != nil {
			renderedContent = messageContent
		}

		// Count lines in both parts plus separator
		linePos += strings.Count(renderedHeader, "\n") + strings.Count(renderedContent, "\n") + 2
		if i < len(m.conversation.Messages)-1 {
			linePos += 2 // separator lines
		}
	}

	// Snap viewport to show selected message at the top
	m.viewport.SetYOffset(linePos)
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
