package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/spf13/cobra"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

var logger otellog.Logger

type Message struct {
	Role      string    `json:"role"`      // "user" or "assistant"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type Conversation struct {
	Name     string    `json:"name"`
	Messages []Message `json:"messages"`
}

func loadConversation(name string) (*Conversation, error) {
	filename := fmt.Sprintf(".%s.figaro.json", name)
	
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// Create new conversation if file doesn't exist
			return &Conversation{
				Name:     name,
				Messages: []Message{},
			}, nil
		}
		return nil, fmt.Errorf("failed to read conversation file: %w", err)
	}

	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("failed to parse conversation file: %w", err)
	}

	logEvent("info", "Loaded conversation", "name", name, "message_count", len(conv.Messages))
	return &conv, nil
}

func (c *Conversation) save() error {
	filename := fmt.Sprintf(".%s.figaro.json", c.Name)
	
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal conversation: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write conversation file: %w", err)
	}

	logEvent("info", "Saved conversation", "name", c.Name, "message_count", len(c.Messages))
	return nil
}

func (c *Conversation) addUserMessage(content string) {
	c.Messages = append(c.Messages, Message{
		Role:      "user",
		Content:   content,
		Timestamp: time.Now(),
	})
}

func (c *Conversation) addAssistantMessage(content string) {
	c.Messages = append(c.Messages, Message{
		Role:      "assistant",
		Content:   content,
		Timestamp: time.Now(),
	})
}

func (c *Conversation) toAnthropicMessages() []anthropic.MessageParam {
	var messages []anthropic.MessageParam
	
	for _, msg := range c.Messages {
		if msg.Role == "user" {
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		} else if msg.Role == "assistant" {
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}
	
	return messages
}

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
	GenerateBlocks(ctx context.Context, messages []anthropic.MessageParam) (<-chan ContentBlock, error)
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

var (
	conversationName string
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "figaro [flags] <message>",
		Short: "Claude CLI with conversation persistence",
		Long:  "A CLI tool to chat with Claude AI with support for persistent conversations",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runFigaro(args)
		},
	}

	rootCmd.Flags().StringVarP(&conversationName, "conversation", "c", "", "Conversation name for persistence (creates .{name}.figaro.json)")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runFigaro(args []string) {
	fmt.Println("=== Claude ===")

	// Setup OpenTelemetry logger
	cleanup := setupLogger()
	defer cleanup()

	// Concatenate all arguments as the prompt
	prompt := strings.Join(args, " ")
	logEvent("info", "Application started", "prompt", prompt, "conversation", conversationName)

	// Load or create conversation
	var conv *Conversation
	var err error
	
	if conversationName != "" {
		conv, err = loadConversation(conversationName)
		if err != nil {
			logEvent("error", "Failed to load conversation", "error", err.Error())
			log.Fatal(err)
		}
	} else {
		// Create temporary conversation for one-off messages
		conv = &Conversation{
			Name:     "temp",
			Messages: []Message{},
		}
	}

	// Add user message to conversation
	conv.addUserMessage(prompt)

	// Save conversation if persistent
	if conversationName != "" {
		if err := conv.save(); err != nil {
			logEvent("error", "Failed to save conversation", "error", err.Error())
			log.Fatal(err)
		}
	}

	// Generate messages for API
	messages := conv.toAnthropicMessages()

	llm, err := NewClaudeLLM()
	if err != nil {
		log.Fatal(err)
		return
	}

	ctx := context.Background()

	blockChan, err := llm.GenerateBlocks(ctx, messages)
	if err != nil {
		logEvent("error", "Failed to generate blocks", "error", err.Error())
		log.Fatal(err)
	}

	logEvent("info", "Starting markdown rendering")

	// Collect response for saving to conversation
	var responseContent strings.Builder

	// Create a new channel to capture response content
	responseChan := make(chan ContentBlock, 10)
	
	// Start a goroutine to capture response content
	go func() {
		defer close(responseChan)
		for block := range blockChan {
			if block.Type == TextBlock {
				responseContent.WriteString(block.Content)
			}
			responseChan <- block
		}
	}()

	if err := RenderMarkdownChannel(responseChan); err != nil {
		logEvent("error", "Failed to render markdown", "error", err.Error())
		log.Fatal(err)
	}

	// Save assistant response to conversation
	if conversationName != "" && responseContent.Len() > 0 {
		conv.addAssistantMessage(responseContent.String())
		if err := conv.save(); err != nil {
			logEvent("error", "Failed to save final conversation", "error", err.Error())
			log.Fatal(err)
		}
	}

	logEvent("info", "Application completed")
}
