package main

import (
	"bufio"
	"fmt"
	"io"

	"github.com/charmbracelet/glamour"
)

// BlockType represents different types of content blocks
type BlockType int

const (
	TextBlock BlockType = iota
	WebSearchBlock
)

// ContentBlock represents a block of content with its type
type ContentBlock struct {
	Type    BlockType
	Content string
}

// RenderMarkdownChannel accepts blocks from a channel and renders them
func RenderMarkdownChannel(blockChan <-chan ContentBlock) error {
	
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		return fmt.Errorf("failed to create renderer: %w", err)
	}

	blockCount := 0
	for block := range blockChan {
		blockCount++
		logEvent("info", "Received block for rendering", "block_count", blockCount, "block_type", int(block.Type))

		switch block.Type {
		case TextBlock:
			logEvent("info", "Rendering text block", "block_count", blockCount, "content_length", len(block.Content))
			
			output, err := renderer.Render(block.Content)
			if err != nil {
				return fmt.Errorf("failed to render text block: %w", err)
			}
			fmt.Print(output)
			
			logEvent("info", "Text block rendered and displayed", "block_count", blockCount)
		case WebSearchBlock:
			logEvent("info", "Rendering web search block", "block_count", blockCount)
			
			// Render web search blocks with special formatting
			searchOutput := fmt.Sprintf("🔍 **Web Search Results:**\n%s\n", block.Content)
			output, err := renderer.Render(searchOutput)
			if err != nil {
				return fmt.Errorf("failed to render web search block: %w", err)
			}
			fmt.Print(output)
			
			logEvent("info", "Web search block rendered and displayed", "block_count", blockCount)
		}
	}

	logEvent("info", "Finished rendering all blocks", "total_blocks_rendered", blockCount)

	return nil
}

// RenderMarkdownStream accepts a stream of markdown text lines and renders them
// continuously until the stream closes. It accumulates lines and renders complete
// blocks when appropriate delimiters are encountered.
func RenderMarkdownStream(reader io.Reader) error {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		return fmt.Errorf("failed to create renderer: %w", err)
	}

	scanner := bufio.NewScanner(reader)
	var markdownBuffer string

	for scanner.Scan() {
		line := scanner.Text()
		if len(markdownBuffer) > 0 {
			markdownBuffer += "\n"
		}
		markdownBuffer += line

		// Render when we encounter an empty line (end of block)
		// or when we detect certain markdown elements that should be rendered immediately
		if shouldRender(line, markdownBuffer) {
			if len(markdownBuffer) > 0 {
				output, err := renderer.Render(markdownBuffer)
				if err != nil {
					return fmt.Errorf("failed to render markdown chunk: %w", err)
				}
				fmt.Print(output)
				markdownBuffer = ""
			}
		}
	}

	// Render any remaining content when stream closes
	if len(markdownBuffer) > 0 {
		output, err := renderer.Render(markdownBuffer)
		if err != nil {
			return fmt.Errorf("failed to render final markdown chunk: %w", err)
		}
		fmt.Print(output)
	}

	return scanner.Err()
}

// shouldRender determines when to render accumulated markdown content
func shouldRender(currentLine, buffer string) bool {
	// Render on empty lines (paragraph breaks)
	if len(currentLine) == 0 {
		return true
	}
	
	// Render immediately for headers
	if len(currentLine) > 0 && currentLine[0] == '#' {
		return true
	}
	
	return false
}
