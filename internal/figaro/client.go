package figaro

import (
	"context"

	"github.com/creachadair/jrpc2"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// Client is a typed JSON-RPC client for talking to a figaro agent socket.
type Client struct {
	cli *jrpc2.Client
}

// DialClient connects to a figaro agent at the given endpoint.
func DialClient(ep transport.Endpoint) (*Client, error) {
	ch, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	cli := jrpc2.NewClient(ch, &jrpc2.ClientOptions{
		// OnNotify handles server-pushed notifications (stream.delta, etc.)
		// The caller sets this via SetNotifyHandler after construction.
	})
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

// Raw returns the underlying jrpc2.Client for advanced usage
// (e.g. setting notification handlers).
func (c *Client) Raw() *jrpc2.Client {
	return c.cli
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.cli.Close()
}
