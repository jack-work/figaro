package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
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

func logEvent(level, message string, attrs ...any) {
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

type Figaro struct {
	llmProvider LLMProvider
}

func NewFigaro() (*Figaro, error) {
	llm, err := NewClaudeLLM()
	if err != nil {
		return nil, err
	}
	return &Figaro{llmProvider: llm}, nil
}

var (
	conversationName string
	forkName         string
	viewMode         bool
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "figaro [flags] <message>",
		Short: "Claude CLI with conversation persistence",
		Long:  "A CLI tool to chat with Claude AI with support for persistent conversations",
		Args:  cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runFigaro(args)
		},
	}

	rootCmd.Flags().StringVarP(&conversationName, "conversation", "c", "", "Conversation name for persistence (creates .{name}.figaro.json)")
	rootCmd.Flags().StringVarP(&forkName, "fork", "f", "", "Fork from existing conversation file")
	rootCmd.Flags().BoolVarP(&viewMode, "view", "v", false, "View full conversation history (requires -c)")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func (f *Figaro) La(args []string, conversationName, forkName string) error {
	fmt.Println("=== Claude ===")

	// Setup OpenTelemetry logger
	cleanup := setupLogger()
	defer cleanup()

	// Concatenate all arguments as the prompt
	prompt := strings.Join(args, " ")
	logEvent("info", "Application started", "prompt", prompt, "conversation", conversationName)

	// Handle fork logic
	if forkName != "" {
		if err := validateForkExists(forkName); err != nil {
			logEvent("error", "Fork validation failed", "error", err.Error())
			return err
		}
		if conversationName == "" {
			return fmt.Errorf("conversation name (-c) is required when forking")
		}
	}

	// Load or create conversation
	var conv *Conversation
	var err error

	if forkName != "" {
		// Create new conversation with parent reference
		conv = &Conversation{
			Name:     conversationName,
			Messages: []Message{},
			Parent:   &forkName,
		}
	} else if conversationName != "" {
		conv, err = loadConversation(conversationName)
		if err != nil {
			logEvent("error", "Failed to load conversation", "error", err.Error())
			return err
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
			return err
		}
	}

	// Generate messages for API
	messages, err := conv.toAnthropicMessages()
	if err != nil {
		logEvent("error", "Failed to generate blocks", "error", err.Error())
		return err
	}

	ctx := context.Background()

	blockChan, err := f.llmProvider.GenerateBlocks(ctx, messages)
	if err != nil {
		logEvent("error", "Failed to generate blocks", "error", err.Error())
		return err
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
		return err
	}

	// Save assistant response to conversation
	if conversationName != "" && responseContent.Len() > 0 {
		conv.addAssistantMessage(responseContent.String())
		if err := conv.save(); err != nil {
			logEvent("error", "Failed to save final conversation", "error", err.Error())
			return err
		}
	}

	logEvent("info", "Application completed")
	return nil
}

func viewConversation(conversationName string) error {
	conv, err := loadConversation(conversationName)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}

	if len(conv.Messages) == 0 {
		fmt.Printf("# Conversation: %s\n\n*No messages yet*\n", conv.Name)
		return nil
	}

	// Create markdown content for the entire conversation
	var content strings.Builder

	// Header with conversation info
	content.WriteString(fmt.Sprintf("# Conversation: %s\n\n", conv.Name))

	if conv.Parent != nil {
		content.WriteString(fmt.Sprintf("**Forked from:** %s\n\n", *conv.Parent))
	}

	content.WriteString(fmt.Sprintf("**Messages:** %d\n\n", len(conv.Messages)))
	content.WriteString("---\n\n")

	// Render each message
	for i, msg := range conv.Messages {
		// Message header with role and timestamp
		roleIcon := "👤"
		if msg.Role == "assistant" {
			roleIcon = "🤖"
		}

		content.WriteString(fmt.Sprintf("## %s **%s** `#%d`\n\n", roleIcon, strings.Title(msg.Role), i+1))
		content.WriteString(fmt.Sprintf("**Time:** %s  \n", msg.Timestamp.Format("2006-01-02 15:04:05")))
		content.WriteString(fmt.Sprintf("**Hash:** `%s`  \n", msg.Hash[:8]))
		if msg.PrevHash != "" {
			content.WriteString(fmt.Sprintf("**Previous:** `%s`  \n", msg.PrevHash[:8]))
		}
		content.WriteString("\n")

		// Message content
		content.WriteString(msg.Content)
		content.WriteString("\n\n---\n\n")
	}

	// Use existing markdown renderer
	blocks := make(chan ContentBlock, 1)
	blocks <- ContentBlock{Type: TextBlock, Content: content.String()}
	close(blocks)

	return RenderMarkdownChannel(blocks)
}

func runFigaro(args []string) {
	if viewMode {
		if conversationName == "" {
			log.Fatal("view mode requires a conversation name (-c)")
		}
		if err := runInteractiveView(conversationName); err != nil {
			log.Fatal(err)
		}
		return
	}

	if len(args) == 0 {
		log.Fatal("message is required when not in view mode")
	}

	figaro, err := NewFigaro()
	if err != nil {
		log.Fatal(err)
	}

	if err := figaro.La(args, conversationName, forkName); err != nil {
		log.Fatal(err)
	}
}
