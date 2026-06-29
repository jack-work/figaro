package angelus

import (
	"context"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/jkrpc"
)

// Client is a typed JSON-RPC client for talking to the angelus supervisor.
// agent:
// this should probably be called ......... idk not cli, it could be
// an sdk.
type Client struct {
	cli *jkrpc.Client
}

// DialClient connects to the angelus at the given endpoint.
func DialClient(ep transport.Endpoint) (*Client, error) {
	conn, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	return &Client{cli: jkrpc.NewClient(conn, nil)}, nil
}

// Create starts a new figaro with the named loadout.
func (c *Client) Create(ctx context.Context, loadout string, patch *rpc.ChalkboardPatch) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.cli.Call(ctx, rpc.MethodCreate, rpc.CreateRequest{Loadout: loadout, Patch: patch}, &resp)
	return &resp, err
}

// Fork branches a conversation: the node freezes and both children get
// fresh system-minted ids. atMainLT == 0 forks at the head; a positive
// value is an interior fork at that IR logical time.
func (c *Client) Fork(ctx context.Context, figaroID string, atMainLT uint64) (*rpc.ForkResponse, error) {
	var resp rpc.ForkResponse
	err := c.cli.Call(ctx, rpc.MethodFork, rpc.ForkRequest{FigaroID: figaroID, AtMainLT: atMainLT}, &resp)
	return &resp, err
}

// CreateEphemeral creates an in-memory-only figaro.
func (c *Client) CreateEphemeral(ctx context.Context, loadout string, patch *rpc.ChalkboardPatch) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.cli.Call(ctx, rpc.MethodCreate, rpc.CreateRequest{
		Loadout: loadout, Patch: patch, Ephemeral: true,
	}, &resp)
	return &resp, err
}

func (c *Client) Kill(ctx context.Context, figaroID string, recursive bool) error {
	return c.cli.Call(ctx, rpc.MethodKill, rpc.KillRequest{FigaroID: figaroID, Recursive: recursive}, nil)
}

func (c *Client) List(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.cli.Call(ctx, rpc.MethodList, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListIDs returns the aria list with only ids populated (skips the expensive
// per-aria chalkboard/forest fills). For completion and other id-only callers.
func (c *Client) ListIDs(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.cli.Call(ctx, rpc.MethodList, rpc.ListRequest{IDsOnly: true}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Attach restores a dormant aria without binding a pid.
func (c *Client) Attach(ctx context.Context, figaroID string) (*rpc.AttachResponse, error) {
	var resp rpc.AttachResponse
	if err := c.cli.Call(ctx, rpc.MethodAttach, rpc.AttachRequest{FigaroID: figaroID}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Bind(ctx context.Context, pid int, figaroID string, atMainLT uint64) error {
	return c.cli.Call(ctx, rpc.MethodBind, rpc.BindRequest{PID: pid, FigaroID: figaroID, AtMainLT: atMainLT}, nil)
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

// SaveBindings persists PID->figaro bindings to disk.
func (c *Client) SaveBindings(ctx context.Context) (*rpc.SaveBindingsResponse, error) {
	var resp rpc.SaveBindingsResponse
	if err := c.cli.Call(ctx, rpc.MethodSaveBindings, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AriaRead fetches IR entries for an aria through the angelus's
// shared LogCache.
func (c *Client) AriaRead(ctx context.Context, figaroID string, from uint64, limit int) (*rpc.AriaReadResponse, error) {
	var resp rpc.AriaReadResponse
	err := c.cli.Call(ctx, rpc.MethodAriaRead, rpc.AriaReadRequest{
		FigaroID: figaroID, From: from, Limit: limit,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Close() error {
	return c.cli.Close()
}
