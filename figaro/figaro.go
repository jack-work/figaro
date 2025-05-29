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
	"go.opentelemetry.io/otel/trace"
)

const FigaroChi = "figaro"

type Figaro struct {
	clients         []mcpClientWrapper
	toolsCache      []mcp.Tool // Might get stale when we implement dynamic tool introduction
	tracerProvider  trace.TracerProvider
	anthropicbridge *anthropicbridge.AnthropicBridge
	update          chan string
}

type ServerRegistry struct {
	DockerServers []dockerbridge.ContainerDefinition `json:"docker_servers"`
}

// Initializes an instance of a Figaro application configured with the provided server list, and returns it.
// Iff error, return val will be nil
// Otherwise, it will always have a non-nil value, even if empty list.
// If server does not return any tools by responding with nil tools in result rather than empty list, that's fine,
// it's interpreted to mean empty list for interest of compatibility.
func SummonFigaro(ctx context.Context, tp trace.TracerProvider, servers ServerRegistry, update chan string) (*Figaro, context.CancelCauseFunc, error) {
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

func (figaro *Figaro) Request(args []string, modePtr *string) error {
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Duration(time.Minute), fmt.Errorf("Operation timed out"))
	defer cancel()

	tracer := figaro.tracerProvider.Tracer("figaro")
	ctx, span := tracer.Start(ctx, "request")
	defer span.End()

	tools := figaro.GetAllTools()
	input := strings.Join(args, " ")

	span.AddEvent("Tools retrieved",
		trace.WithAttributes(attribute.String("serialized_tools", logging.EzMarshal(tools))))

	role := anthropic.MessageParamRole(string(anthropic.MessageParamRoleUser))

	conversation := make([]anthropic.MessageParam, 0, 1)
	anthropicClient, err := anthropicbridge.InitAnthropic(anthropicbridge.WithLogging(figaro.tracerProvider))
	if err != nil {
		return fmt.Errorf("failed to initialize Anthropic client: %w", err)
	}

	for range 1 {
		conversation = append(conversation, anthropic.MessageParam{
			Content: []anthropic.ContentBlockParamUnion{{
				OfRequestTextBlock: &anthropic.TextBlockParam{Text: input},
			}},
			Role: role,
		})
		messageParams := GetMessageNewParams(conversation, tools)
		stream, err := anthropicClient.StreamMessage(context.Background(), *messageParams)
		if err != nil {
			return err
		}

		var message *anthropic.Message
	progressLoop:
		for {
			select {
			case err := <-stream.Error:
				cancel()
				return err
			case next := <-stream.Progress:
				figaro.update <- next
			case message = <-stream.Result:
				figaro.update <- "\n\n"
				break progressLoop
			}
		}

		conversation = appendMessage(conversation, message)

		for message.StopReason == "tool_use" {
			toolResponses, err := callTools(ctx, message, figaro)
			if err != nil {
				return err
			}

			toolResults := make([]anthropic.ContentBlockParamUnion, 0)
			for id, toolResponse := range toolResponses {
				toolResults = append(toolResults, anthropic.ContentBlockParamUnion{
					OfRequestToolResultBlock: &anthropic.ToolResultBlockParam{
						ToolUseID: id,
						Content: []anthropic.ToolResultBlockParamContentUnion{{
							OfRequestTextBlock: &anthropic.TextBlockParam{
								Text: anyToString(toolResponse.Result),
							},
						}},
					},
				})
			}

			conversation = append(conversation, anthropic.MessageParam{
				Content: toolResults,
				Role:    anthropic.MessageParamRoleUser,
			})

			messageParams = GetMessageNewParams(conversation, tools)
			stream, err = anthropicClient.StreamMessage(ctx, *messageParams)
			if err != nil {
				cancel()
				return err
			}

		progressLoop2:
			for {
				select {
				case err := <-stream.Error:
					cancel()
					return err
				case next, ok := <-stream.Progress:
					if !ok {
						// Progress channel closed, move on to result
						break progressLoop2
					}
					// fmt.Print(next)
					figaro.update <- next
				case message = <-stream.Result:
					break progressLoop2
				}
			}

		}

		conversation = appendMessage(conversation, message)

		// fmt.Println("done!")
		figaro.update <- "\n\ndone!"
		if err := writeHostFile(conversation, ".conversation.json"); err != nil {
			span.RecordError(err)
			logging.EzPrint(err)
		}
	}

	return nil
}

func appendMessage(conversation []anthropic.MessageParam, message *anthropic.Message) []anthropic.MessageParam {
	modelResponse := make([]anthropic.ContentBlockParamUnion, 0, len(message.Content))
	for _, message := range message.Content {
		modelResponse = append(modelResponse, message.ToParam())
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
