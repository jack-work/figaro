package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/joho/godotenv"
)

type LLMProvider interface {
	GenerateBlocks(ctx context.Context, messages []anthropic.MessageParam) (<-chan ContentBlock, error)
}

type ClaudeLLM struct {
	client *anthropic.Client
}

func NewClaudeLLM() (*ClaudeLLM, error) {
	godotenv.Load()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	return &ClaudeLLM{client: &client}, nil
}

func (c *ClaudeLLM) GenerateBlocks(ctx context.Context, messages []anthropic.MessageParam) (<-chan ContentBlock, error) {
	blockChan := make(chan ContentBlock, 10)

	go func() {
		defer close(blockChan)

		// Log start of request
		logEvent("info", "Starting LLM request", "message_count", len(messages))

		resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.ModelClaudeSonnet4_20250514,
			MaxTokens: 1024,
			Messages:  messages,
			Tools: []anthropic.ToolUnionParam{{
				OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
					MaxUses: param.Opt[int64]{
						Value: 5,
					},
				},
			}},
		})

		// Log response received
		logEvent("info", "Received LLM response", "has_error", err != nil)

		if err != nil {
			logEvent("error", "LLM request failed", "error", err.Error())
			// Send error as text block
			blockChan <- ContentBlock{
				Type:    TextBlock,
				Content: fmt.Sprintf("Error: %v", err),
			}
			return
		}

		// Process all content blocks from the response
		for i, content := range resp.Content {
			logEvent("info", "Processing content block", "block_index", i)

			switch v := content.AsAny().(type) {
			case anthropic.TextBlock:
				preview := strings.ReplaceAll(v.Text[:min(50, len(v.Text))], "\n", "\\n")
				logEvent("info", "Sending text block", "block_index", i, "content_length", len(v.Text), "content_preview", preview)
				blockChan <- ContentBlock{
					Type:    TextBlock,
					Content: v.Text,
				}
			case anthropic.ServerToolUseBlock:
				logEvent("info", "Sending server tool use block", "block_index", i)
				blockChan <- ContentBlock{
					Type:    WebSearchBlock,
					Content: v.RawJSON(),
				}
			case anthropic.WebSearchToolResultBlock:
				logEvent("info", "Sending web search result block", "block_index", i)
				blockChan <- ContentBlock{
					Type:    WebSearchBlock,
					Content: v.Content.RawJSON(),
				}
			default:
				logEvent("warn", "Unknown content block type", "block_index", i, "type", fmt.Sprintf("%T", v))
				fmt.Printf("Unknown content block type: %T\n", v)
			}
		}

		logEvent("info", "Finished processing all blocks", "total_blocks", len(resp.Content))
	}()

	return blockChan, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
