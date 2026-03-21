package angelus

import (
	"context"

	"github.com/creachadair/jrpc2"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// Client is a typed JSON-RPC client for talking to the angelus supervisor.
type Client struct {
	cli *jrpc2.Client
}

// DialClient connects to the angelus at the given endpoint.
func DialClient(ep transport.Endpoint) (*Client, error) {
	ch, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	return &Client{cli: jrpc2.NewClient(ch, nil)}, nil
}

// Create creates a new figaro and returns its ID and endpoint.
func (c *Client) Create(ctx context.Context, provider, model string) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.cli.CallResult(ctx, rpc.MethodCreate, rpc.CreateRequest{
		Provider: provider, Model: model,
	}, &resp)
	return &resp, err
}

// Kill kills a figaro.
func (c *Client) Kill(ctx context.Context, figaroID string) error {
	_, err := c.cli.Call(ctx, rpc.MethodKill, rpc.KillRequest{FigaroID: figaroID})
	return err
}

// List returns info for all figaros.
func (c *Client) List(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.cli.CallResult(ctx, rpc.MethodList, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Bind maps a pid to a figaro.
func (c *Client) Bind(ctx context.Context, pid int, figaroID string) error {
	_, err := c.cli.Call(ctx, rpc.MethodBind, rpc.BindRequest{PID: pid, FigaroID: figaroID})
	return err
}

// Resolve looks up which figaro a pid is bound to.
func (c *Client) Resolve(ctx context.Context, pid int) (*rpc.ResolveResponse, error) {
	var resp rpc.ResolveResponse
	if err := c.cli.CallResult(ctx, rpc.MethodResolve, rpc.ResolveRequest{PID: pid}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Unbind removes a pid binding.
func (c *Client) Unbind(ctx context.Context, pid int) error {
	_, err := c.cli.Call(ctx, rpc.MethodUnbind, rpc.UnbindRequest{PID: pid})
	return err
}

// Status returns angelus status.
func (c *Client) Status(ctx context.Context) (*rpc.StatusResponse, error) {
	var resp rpc.StatusResponse
	if err := c.cli.CallResult(ctx, rpc.MethodStatus, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.cli.Close()
}
