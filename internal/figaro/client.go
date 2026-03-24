package figaro

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/internal/jsonrpc"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// NotifyHandler is called for each server-pushed notification.
// Called synchronously on the reader goroutine — in wire order.
type NotifyHandler func(method string, params json.RawMessage)

// Client is a typed JSON-RPC client for talking to a figaro agent socket.
type Client struct {
	cli *jsonrpc.Client
}

// DialClient connects to a figaro agent at the given endpoint.
// onNotify is called for each notification, in wire order. May be nil.
func DialClient(ep transport.Endpoint, onNotify NotifyHandler) (*Client, error) {
	conn, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	cli := jsonrpc.NewClient(conn, jsonrpc.NotifyFunc(onNotify))
	return &Client{cli: cli}, nil
}

// Prompt sends a prompt to the figaro and returns immediately (enqueued).
func (c *Client) Prompt(ctx context.Context, text string) error {
	return c.cli.Call(ctx, rpc.MethodPrompt, rpc.PromptRequest{Text: text}, nil)
}

// Context returns all messages in the figaro's chat history.
func (c *Client) Context(ctx context.Context) (*rpc.ContextResponse, error) {
	var resp rpc.ContextResponse
	if err := c.cli.Call(ctx, rpc.MethodContext, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Info returns the figaro's metadata.
func (c *Client) Info(ctx context.Context) (*rpc.FigaroInfoResponse, error) {
	var resp rpc.FigaroInfoResponse
	if err := c.cli.Call(ctx, rpc.MethodFigaroInfo, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SetModel changes the figaro's model.
func (c *Client) SetModel(ctx context.Context, model string) error {
	return c.cli.Call(ctx, rpc.MethodSetModel, rpc.SetModelRequest{Model: model}, nil)
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.cli.Close()
}
