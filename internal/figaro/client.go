package figaro

import (
	"context"
	"encoding/json"

	"github.com/jack-work/jkrpc"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// NotifyHandler handles server-pushed notifications (wire order).
type NotifyHandler func(method string, params json.RawMessage)

// Client is a typed JSON-RPC client for talking to a figaro agent socket.
type Client struct {
	cli *jkrpc.Client
}

// DialClient connects to a figaro agent.
func DialClient(ep transport.Endpoint, onNotify NotifyHandler) (*Client, error) {
	conn, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	cli := jkrpc.NewClient(conn, jkrpc.NotifyFunc(onNotify))
	return &Client{cli: cli}, nil
}

// Qua sends a prompt and returns the index its user tic occupies.
func (c *Client) Qua(ctx context.Context, text string, cb *rpc.ChalkboardInput) (uint64, error) {
	var resp rpc.QuaResponse
	if err := c.cli.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: text, Chalkboard: cb}, &resp); err != nil {
		return 0, err
	}
	return resp.Index, nil
}

// Read fetches a windowed slice of the aria log (and the open tail).
// With req.Follow, live log.* frames continue arriving on this
// connection's notify handler after the catch-up batch returns.
func (c *Client) Read(ctx context.Context, req rpc.ReadRequest) (*rpc.ReadResponse, error) {
	var resp rpc.ReadResponse
	if err := c.cli.Call(ctx, rpc.MethodRead, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Context returns all messages in the figaro's chat history.
func (c *Client) Context(ctx context.Context) (*rpc.ContextResponse, error) {
	var resp rpc.ContextResponse
	if err := c.cli.Call(ctx, rpc.MethodContext, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Interrupt asks the figaro to abort its current turn.
func (c *Client) Interrupt(ctx context.Context) error {
	return c.cli.Call(ctx, rpc.MethodInterrupt, rpc.InterruptRequest{}, nil)
}

// Set applies a chalkboard patch directly. No LLM round-trip.
func (c *Client) Set(ctx context.Context, patch rpc.ChalkboardPatch) (*rpc.SetResponse, error) {
	var resp rpc.SetResponse
	if err := c.cli.Call(ctx, rpc.MethodSet, rpc.SetRequest{Patch: patch}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Loadout applies a named loadout additively to the chalkboard. No
// keys are removed; values equal to the current snapshot are skipped.
func (c *Client) Loadout(ctx context.Context, name string) (*rpc.LoadoutResponse, error) {
	var resp rpc.LoadoutResponse
	if err := c.cli.Call(ctx, rpc.MethodLoadout, rpc.LoadoutRequest{Name: name}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Chalkboard returns the agent's current chalkboard snapshot.
func (c *Client) Chalkboard(ctx context.Context) (*rpc.ChalkboardResponse, error) {
	var resp rpc.ChalkboardResponse
	if err := c.cli.Call(ctx, rpc.MethodChalkboard, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.cli.Close()
}

// Done returns a channel closed when the connection dies.
func (c *Client) Done() <-chan struct{} {
	return c.cli.Done()
}
