package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

var logger otellog.Logger

func setupLogger() func() {
	ctx := context.Background()
	
	// Create a log file
	logFile, err := os.Create("llm_output.jsonl")
	if err != nil {
		log.Fatal("Failed to create log file:", err)
	}

	// Create resource
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("figaro"),
		semconv.ServiceVersion("1.0.0"),
	)

	// Create stdoutlog exporter with file writer
	exp, err := stdoutlog.New(
		stdoutlog.WithWriter(logFile),
		stdoutlog.WithPrettyPrint(),
	)
	if err != nil {
		log.Fatal("Failed to create exporter:", err)
	}

	// Create processor and provider
	processor := sdklog.NewBatchProcessor(exp)
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(processor),
		sdklog.WithResource(res),
	)

	// Set global logger provider
	global.SetLoggerProvider(provider)
	
	// Get logger
	logger = provider.Logger("figaro-logger")

	return func() {
		if provider != nil {
			provider.Shutdown(ctx)
		}
		if logFile != nil {
			logFile.Close()
		}
	}
}

func logEvent(level, message string, attrs ...interface{}) {
	if logger == nil {
		return
	}

	var severity otellog.Severity
	var severityText string
	
	switch level {
	case "info":
		severity = otellog.SeverityInfo
		severityText = "INFO"
	case "error":
		severity = otellog.SeverityError
		severityText = "ERROR"
	case "warn":
		severity = otellog.SeverityWarn
		severityText = "WARN"
	default:
		severity = otellog.SeverityInfo
		severityText = "INFO"
	}

	// Create log record
	record := otellog.Record{}
	record.SetTimestamp(time.Now())
	record.SetObservedTimestamp(time.Now())
	record.SetSeverity(severity)
	record.SetSeverityText(severityText)
	record.SetBody(otellog.StringValue(message))

	// Add attributes
	for i := 0; i < len(attrs); i += 2 {
		if i+1 < len(attrs) {
			if key, ok := attrs[i].(string); ok {
				switch v := attrs[i+1].(type) {
				case string:
					record.AddAttributes(otellog.String(key, v))
				case int:
					record.AddAttributes(otellog.Int64(key, int64(v)))
				case int64:
					record.AddAttributes(otellog.Int64(key, v))
				case bool:
					record.AddAttributes(otellog.Bool(key, v))
				default:
					record.AddAttributes(otellog.String(key, fmt.Sprintf("%v", v)))
				}
			}
		}
	}

	logger.Emit(context.Background(), record)
}

type LLMProvider interface {
	GenerateText(ctx context.Context, prompt string) (io.Reader, error)
	GenerateBlocks(ctx context.Context, prompt string) (<-chan ContentBlock, error)
}

type ClaudeLLM struct {
	client *anthropic.Client
}

func NewClaudeLLM() (*ClaudeLLM, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	return &ClaudeLLM{client: &client}, nil
}

func (c *ClaudeLLM) GenerateText(ctx context.Context, prompt string) (io.Reader, error) {
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeSonnet4_20250514,
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		Tools: []anthropic.ToolUnionParam{{
			OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
				MaxUses: param.Opt[int64]{
					Value: 5,
				},
			},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call Anthropic API: %w", err)
	}

	if len(resp.Content) > 0 {
		textBlock := resp.Content[0].AsText()
		return strings.NewReader(textBlock.Text), nil
	}

	return strings.NewReader(""), nil
}

func (c *ClaudeLLM) GenerateBlocks(ctx context.Context, prompt string) (<-chan ContentBlock, error) {
	blockChan := make(chan ContentBlock, 10)

	go func() {
		defer close(blockChan)

		// Log start of request
		logEvent("info", "Starting LLM request", "prompt_length", len(prompt))

		resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.ModelClaudeSonnet4_20250514,
			MaxTokens: 1024,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
			},
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

func main() {
	fmt.Println("=== Claude ===")

	// Setup OpenTelemetry logger
	cleanup := setupLogger()
	defer cleanup()

	llm, err := NewClaudeLLM()
	if err != nil {
		log.Fatal(err)
		return
	}

	ctx := context.Background()

	if len(os.Args) == 0 {
		log.Fatal(err)
		return
	}

	// Concatenate all command line arguments as the prompt
	var prompt string
	prompt = strings.Join(os.Args[1:], " ")

	logEvent("info", "Application started", "prompt", prompt)

	blockChan, err := llm.GenerateBlocks(ctx, prompt)
	if err != nil {
		logEvent("error", "Failed to generate blocks", "error", err.Error())
		log.Fatal(err)
	}

	logEvent("info", "Starting markdown rendering")

	if err := RenderMarkdownChannel(blockChan); err != nil {
		logEvent("error", "Failed to render markdown", "error", err.Error())
		log.Fatal(err)
	}

	logEvent("info", "Application completed")
}
