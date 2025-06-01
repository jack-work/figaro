package figaro

import (
	"context"
	"encoding/json"
	"figaro/anthropicbridge"
	"figaro/dockerbridge"
	"figaro/jsonrpc"
	"figaro/logging"
	"figaro/mcp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const FigaroChi = "figaro"

type Figaro struct {
	clients         []mcpClientWrapper
	toolsCache      []mcp.Tool // Might get stale when we implement dynamic tool introduction
	tracerProvider  trace.TracerProvider
	anthropicbridge *anthropicbridge.AnthropicBridge
	update          chan Event
}

type EventType string

const (
	TaskStarted   EventType = "task_started"
	TaskCompleted EventType = "task_completed"
	TaskFailed    EventType = "task_failed"

	MessageStarted EventType = "message_started"
	MessagePart    EventType = "message_part"
	MessageEnded   EventType = "message_ended"
)

type Event struct {
	Type      EventType              `json:"type"`
	TaskID    string                 `json:"task_id"`
	MessageID string                 `json:"message_id"`
	Data      string                 `json:"data,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type ServerRegistry struct {
	DockerServers []dockerbridge.ContainerDefinition `json:"docker_servers"`
}

// Initializes an instance of a Figaro application configured with the provided server list, and returns it.
// Iff error, return val will be nil
// Otherwise, it will always have a non-nil value, even if empty list.
// If server does not return any tools by responding with nil tools in result rather than empty list, that's fine,
// it's interpreted to mean empty list for interest of compatibility.
func SummonFigaro(ctx context.Context, tp trace.TracerProvider, servers ServerRegistry, update chan Event) (*Figaro, context.CancelCauseFunc, error) {
	ctx, cancel := context.WithCancelCause(ctx)

	tracer := tp.Tracer("figaro")
	ctx, span := tracer.Start(ctx, "summonfigaro")
	defer span.End()

	mcpClients := make([]mcpClientWrapper, len(servers.DockerServers))
	for i, server := range servers.DockerServers {
		// parent context for each pair
		serviceContext := context.WithoutCancel(ctx)

		// child context for connection
		connCtx, cancelConn := context.WithCancel(serviceContext)

		// TODO: Manage lifecycle instead of taking down the whole server
		go func() {
			for {
				select {
				case <-ctx.Done():
					cancelConn()
					break
				case <-connCtx.Done():
					cancel(connCtx.Err())
					break
				}
			}
		}()

		connection, connectionDone, err := dockerbridge.Setup(connCtx, server, tp)
		if err != nil {
			cancel(err)
			cancelConn()
			return nil, nil, err
		}

		// child context for client
		rpcCtx, cancelRpc := context.WithCancel(serviceContext)

		// TODO: Manage lifecycle instead of taking down the whole server
		go func() {
			for {
				select {
				case <-ctx.Done():
					cancelConn()
					break
				case <-rpcCtx.Done():
					cancel(rpcCtx.Err())
					break
				}
			}
		}()

		client, rpcDone, err := jsonrpc.NewStdioClient[string](rpcCtx, connection, tp)
		if err != nil {
			cancelConn()
			cancelRpc()
			cancel(err)
			return nil, nil, err
		}

		mcpClient, err := mcp.Initialize(ctx, server, client, tp)
		if err != nil {
			cancelConn()
			cancelRpc()
			cancel(err)
			return nil, nil, err
		}

		// add failure logging at this level
		mcpClients[i] = mcpClientWrapper{
			mcpClient: mcpClient,
			connection: &lifeCycleWrapper{
				done:   connectionDone,
				cancel: cancelConn,
			},
			rpcClient: &serviceWrapper[jsonrpc.Client]{
				instance: client,
				done:     rpcDone,
				cancel:   cancelRpc,
			},
		}
	}

	return &Figaro{
		clients:        mcpClients,
		tracerProvider: tp,
		update:         update,
	}, cancel, nil
}

type serviceWrapper[T any] struct {
	instance T
	done     <-chan error
	cancel   context.CancelFunc
}

type lifeCycleWrapper struct {
	done   <-chan error
	cancel context.CancelFunc
}

type mcpClientWrapper struct {
	mcpClient  *mcp.Client
	connection *lifeCycleWrapper
	rpcClient  *serviceWrapper[jsonrpc.Client]
}

func (figaro *Figaro) GetClientForTool(toolName string) *mcp.Client {
	for _, clientWrapper := range figaro.clients {
		client := clientWrapper.mcpClient
		if client.ContainsTool(toolName) {
			return client
		}
	}
	return nil
}

func (figaro *Figaro) GetAllTools() []mcp.Tool {
	if figaro.toolsCache != nil {
		return figaro.toolsCache
	}
	cumToolSize := 0
	for _, clientWrapper := range figaro.clients {
		client := clientWrapper.mcpClient
		cumToolSize += len(client.Tools)
	}
	result := make([]mcp.Tool, cumToolSize)
	for i, clientWrapper := range figaro.clients {
		client := clientWrapper.mcpClient
		for j, tool := range client.Tools {
			result[i+j] = tool
		}
	}
	figaro.toolsCache = result
	return result
}

func (figaro *Figaro) handleStream(ctx context.Context, stream *anthropicbridge.ConsoleStreamable[*anthropic.Message], cancel context.CancelFunc, addSuffix bool) (*anthropic.Message, error) {
	hasStarted := false
	for {
		select {
		case err := <-stream.Error:
			cancel()
			return nil, err
		case next, ok := <-stream.Progress:
			if !ok {
				return <-stream.Result, nil
			}
			var evtType EventType
			if !hasStarted {
				evtType = MessageStarted
			} else {
				evtType = MessagePart
			}
			figaro.update <- Event{Type: evtType, Data: next}
		case message := <-stream.Result:
			figaro.update <- Event{Type: MessageEnded, MessageID: message.ID}
			return message, nil
		}
	}
}

func (figaro *Figaro) Request(args []string, modePtr *string) error {
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Duration(time.Minute), fmt.Errorf("Operation timed out"))
	defer cancel()

	tracer := figaro.tracerProvider.Tracer("figaro")
	ctx, span := tracer.Start(ctx, "request")
	defer span.End()

	conversation := make([]anthropic.MessageParam, 0, 1)
	anthropicClient, err := anthropicbridge.InitAnthropic(anthropicbridge.WithLogging(figaro.tracerProvider))
	if err != nil {
		return fmt.Errorf("failed to initialize Anthropic client: %w", err)
	}
	role := anthropic.MessageParamRole(string(anthropic.MessageParamRoleUser))
	input := strings.Join(args, " ")

	messageParam := createMessageParam(input, role)
	conversation = append(conversation, messageParam)

	message, conversation, err := figaro.streamMessage(ctx, conversation, anthropicClient, cancel)
	if err != nil {
		return err
	}

	err = figaro.agentLoop(ctx, span, anthropicClient, message, conversation, cancel)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(attribute.String("error_details", logging.EzMarshal(err)))
	}
	return err
}
func (figaro *Figaro) agentLoop(
	ctx context.Context,
	span trace.Span,
	anthropicClient anthropicbridge.AnthropicBridge,
	message *anthropic.Message,
	conversation []anthropic.MessageParam,
	cancel context.CancelFunc,
) error {
	i := 0
	for {
		if err := shouldDisrupt(i); err != nil {
			return err
		}
		if message.StopReason == "tool_use" {
			toolResponses, err := callTools(ctx, message, figaro)
			if err != nil {
				return err
			}

			toolResults := make([]anthropic.ContentBlockParamUnion, 0)
			for id, toolResponse := range toolResponses {
				block := createToolResultBlock(id, toolResponse)
				toolResults = append(toolResults, block)
			}

			conversation = append(conversation, anthropic.MessageParam{
				Content: toolResults,
				Role:    anthropic.MessageParamRoleUser,
			})

			message, conversation, err = figaro.streamMessage(ctx, conversation, anthropicClient, cancel)
			if err != nil {
				return err
			}

			if err := writeHostFile(conversation, ".conversation.json"); err != nil {
				span.RecordError(err)
				logging.EzPrint(err)
			}
		} else {
			return nil
		}
		i++
	}
}

// Returns an error object if the agent loop should be disrupted.
func shouldDisrupt(i int) error {
	if i < 10 {
		return nil
	}
	return fmt.Errorf("Maximum iteration count was exhausted.")
}

func createToolResultBlock(id string, toolResponse jsonrpc.Message[any]) anthropic.ContentBlockParamUnion {
	newVar := anthropic.ContentBlockParamUnion{
		OfRequestToolResultBlock: &anthropic.ToolResultBlockParam{
			ToolUseID: id,
			Content: []anthropic.ToolResultBlockParamContentUnion{{
				OfRequestTextBlock: &anthropic.TextBlockParam{
					Text: anyToString(toolResponse.Result),
				},
			}},
		},
	}
	return newVar
}

func createMessageParam(input string, role anthropic.MessageParamRole) anthropic.MessageParam {
	messageParam := anthropic.MessageParam{
		Content: []anthropic.ContentBlockParamUnion{{
			OfRequestTextBlock: &anthropic.TextBlockParam{Text: input},
		}},
		Role: role,
	}
	return messageParam
}

// This method calls the llm stream api and then pipes the response stream to that which
// is configured in the receiver.
func (figaro *Figaro) streamMessage(
	ctx context.Context,
	conversation []anthropic.MessageParam,
	anthropicClient anthropicbridge.AnthropicBridge,
	cancel context.CancelFunc,
) (*anthropic.Message, []anthropic.MessageParam, error) {
	tools := figaro.GetAllTools()
	messageParams := GetMessageNewParams(conversation, tools)
	stream, err := anthropicClient.StreamMessage(ctx, *messageParams)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	message, err := figaro.handleStream(ctx, stream, cancel, true)
	if err != nil {
		return nil, nil, err
	}
	conversation = appendMessage(conversation, message)
	return message, conversation, nil
}

func appendMessage(conversation []anthropic.MessageParam, message *anthropic.Message) []anthropic.MessageParam {
	modelResponse := make([]anthropic.ContentBlockParamUnion, 0, len(message.Content))
	for _, content := range message.Content {
		modelResponse = append(modelResponse, content.ToParam())
	}
	conversation = append(conversation, anthropic.MessageParam{
		Content: modelResponse,
		Role:    anthropic.MessageParamRoleAssistant,
	})
	return conversation
}

func writeHostFile(contents any, path ...string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	relative := filepath.Join(path...)
	filePath := filepath.Join(homeDir, ".figaro", relative)
	byteContents, err := json.Marshal(contents)
	err = os.WriteFile(filePath, byteContents, os.FileMode(os.O_TRUNC))
	if err != nil {
		return err
	}

	return nil
}

func GetMessageNewParams(conversation []anthropic.MessageParam, tools []mcp.Tool) *anthropic.MessageNewParams {
	anthropicTools := anthropicbridge.GetAnthropicTools(tools)
	messageParams := &anthropic.MessageNewParams{
		MaxTokens: 1024,
		Messages:  conversation,
		Model:     anthropic.ModelClaude3_7SonnetLatest,
		Tools:     anthropicTools,
	}
	return messageParams
}

func callTools(ctx context.Context, message *anthropic.Message, figaro *Figaro) (map[string]jsonrpc.Message[any], error) {
	tools := make(map[string]jsonrpc.Message[any], len(message.Content))
	for _, block := range message.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ToolUseBlock:
			client := figaro.GetClientForTool(variant.Name)
			if client == nil {
				return nil, fmt.Errorf("Could not find mcp client for %v", variant.Name)
			}
			var args map[string]any
			err := json.Unmarshal(variant.Input, &args)
			if err != nil {
				return nil, err
			}
			response, err := client.SendMessage(
				ctx,
				"tools/call",
				mcp.CallToolRequestParams{
					Name:      variant.Name,
					Arguments: args,
				})
			if err != nil {
				return nil, err
			}
			tools[variant.ID] = *response
		}
	}
	return tools, nil
}

// Converting to string
func anyToString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]any, []any:
		bytes, _ := json.Marshal(val)
		return string(bytes)
	default:
		return fmt.Sprintf("%v", val)
	}
}
