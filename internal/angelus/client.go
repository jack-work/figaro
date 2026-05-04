package angelus

import (
	"context"

	"github.com/jack-work/figaro/internal/jsonrpc"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// Client is a typed JSON-RPC client for talking to the angelus supervisor.
type Client struct {
	cli *jsonrpc.Client
}

// DialClient connects to the angelus at the given endpoint.
func DialClient(ep transport.Endpoint) (*Client, error) {
	conn, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	return &Client{cli: jsonrpc.NewClient(conn, nil)}, nil
}

func (c *Client) Create(ctx context.Context, provider, model string) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.cli.Call(ctx, rpc.MethodCreate, rpc.CreateRequest{Provider: provider, Model: model}, &resp)
	return &resp, err
}

// CreateEphemeral creates a figaro whose state lives in memory only.
// No aria file is written; the agent vanishes when killed.
func (c *Client) CreateEphemeral(ctx context.Context, provider, model string) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.cli.Call(ctx, rpc.MethodCreate, rpc.CreateRequest{
		Provider: provider, Model: model, Ephemeral: true,
	}, &resp)
	return &resp, err
}

func (c *Client) Kill(ctx context.Context, figaroID string) error {
	return c.cli.Call(ctx, rpc.MethodKill, rpc.KillRequest{FigaroID: figaroID}, nil)
}

func (c *Client) List(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.cli.Call(ctx, rpc.MethodList, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Bind(ctx context.Context, pid int, figaroID string) error {
	return c.cli.Call(ctx, rpc.MethodBind, rpc.BindRequest{PID: pid, FigaroID: figaroID}, nil)
}

func (c *Client) Resolve(ctx context.Context, pid int) (*rpc.ResolveResponse, error) {
	var resp rpc.ResolveResponse
	if err := c.cli.Call(ctx, rpc.MethodResolve, rpc.ResolveRequest{PID: pid}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Unbind(ctx context.Context, pid int) error {
	return c.cli.Call(ctx, rpc.MethodUnbind, rpc.UnbindRequest{PID: pid}, nil)
}

// SaveBindings asks the angelus to persist its current PID→figaro
// bindings to disk. Called by `figaro rest --keep-pids` just before
// sending SIGTERM so the bindings survive the restart.
func (c *Client) SaveBindings(ctx context.Context) (*rpc.SaveBindingsResponse, error) {
	var resp rpc.SaveBindingsResponse
	if err := c.cli.Call(ctx, rpc.MethodSaveBindings, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Close() error {
	return c.cli.Close()
}
