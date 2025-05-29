package mcp

import (
	"context"
	"figaro/dockerbridge"
	"figaro/jsonrpc"
	"figaro/logging"
	"fmt"

	"github.com/mitchellh/mapstructure"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Server interface {
	GetEnv() *[]string
}

type Client struct {
	jsonrpc.StdioClient `json:"-"`
	TargetServer        Server
	Tools               []Tool // TODO: replace with interface method and implement a cache with updates
	TracerProvider      trace.TracerProvider
}

func (client *Client) ContainsTool(toolName string) bool {
	for _, tool := range client.Tools {
		if tool.Name == toolName {
			return true
		}
	}
	return false
}

// executes mcp handshake and initializes tools
func Initialize(ctx context.Context, server dockerbridge.ContainerDefinition, rpcClient *jsonrpc.StdioClient, tp trace.TracerProvider) (*Client, error) {
	client := createMcpClient(server, rpcClient, tp)

	tracer := tp.Tracer("mcp.Initialize")
	ctx, span := tracer.Start(ctx, "mcp.Initialize")
	defer span.End()

	res1, err := client.SendMessage(ctx,
		"initialize", InitializeRequestParams{
			ProtocolVersion: "0.1.0",
			ClientInfo: Implementation{
				Name:    "figaro",
				Version: "1.0.0",
			},
			Capabilities: ClientCapabilities{},
		})
	if err != nil {
		span.AddEvent("Error when calling initialize", trace.WithStackTrace(true))
		return nil, err
	}

	span.AddEvent("Initialize response", trace.WithAttributes(
		attribute.String("res1", logging.EzMarshal(res1))))

	err = client.Notify(ctx, "notifications/initialized", InitializedNotification{})
	if err != nil {
		return nil, err
	}

	// need to impl pagination
	untypedToolsResponse, err := client.SendActionMessage(ctx, "tools/list")
	if err != nil {
		return nil, err
	}

	var toolsResult ListToolsResult
	err = mapstructure.Decode(untypedToolsResponse.Result, &toolsResult)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	client.Tools = toolsResult.Tools
	return &client, nil
}

func createMcpClient(server dockerbridge.ContainerDefinition, client *jsonrpc.StdioClient, tp trace.TracerProvider) Client {
	return Client{
		StdioClient:    *client,
		TargetServer:   server,
		TracerProvider: tp,
	}
}
