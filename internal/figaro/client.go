package figaro

import (
	"context"

	"github.com/creachadair/jrpc2"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// NotifyHandler is called for each server-pushed notification.
// The method is one of the rpc.Method* constants (stream.delta, etc.)
// and req can be used to unmarshal the params.
type NotifyHandler func(method string, req *jrpc2.Request)

// Client is a typed JSON-RPC client for talking to a figaro agent socket.
type Client struct {
	cli *jrpc2.Client
}

// DialClient connects to a figaro agent at the given endpoint.
// If onNotify is non-nil, it will be called for each server-pushed
// notification (stream.delta, stream.done, etc.).
func DialClient(ep transport.Endpoint, onNotify NotifyHandler) (*Client, error) {
	ch, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	var opts *jrpc2.ClientOptions
	if onNotify != nil {
		opts = &jrpc2.ClientOptions{
			OnNotify: func(req *jrpc2.Request) {
				onNotify(req.Method(), req)
			},
		}
	}
	cli := jrpc2.NewClient(ch, opts)
	return &Client{cli: cli}, nil
}

// Prompt sends a prompt to the figaro and returns immediately (enqueued).
func (c *Client) Prompt(ctx context.Context, text string) error {
	_, err := c.cli.Call(ctx, rpc.MethodPrompt, rpc.PromptRequest{Text: text})
	return err
}

// Context returns all messages in the figaro's chat history.
func (c *Client) Context(ctx context.Context) (*rpc.ContextResponse, error) {
	var resp rpc.ContextResponse
	if err := c.cli.CallResult(ctx, rpc.MethodContext, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Info returns the figaro's metadata.
func (c *Client) Info(ctx context.Context) (*rpc.FigaroInfoResponse, error) {
	var resp rpc.FigaroInfoResponse
	if err := c.cli.CallResult(ctx, rpc.MethodFigaroInfo, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SetModel changes the figaro's model.
func (c *Client) SetModel(ctx context.Context, model string) error {
	_, err := c.cli.Call(ctx, rpc.MethodSetModel, rpc.SetModelRequest{Model: model})
	return err
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.cli.Close()
}
