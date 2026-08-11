package figaro

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/jkrpc"
)

// NotifyHandler handles server-pushed notifications (wire order).
type NotifyHandler func(method string, params json.RawMessage)

// Client is a typed JSON-RPC client for talking to a figaro agent socket.
type Client struct {
	cli *jkrpc.Client
	// caller is the aria this process belongs to; see angelus.Client.caller.
	// The identity must survive BOTH hops (CLI -> angelus, angelus -> agent),
	// and these are separate clients over separate sockets, so each presents
	// it independently.
	caller string
	// ref is the ASSERTED caller reference (the duke placeholder, or an
	// explicit FIGARO_CALLER label). Attribution only, never a credential.
	ref *rpc.CallerRef
}

// call is the single point every request passes through; see
// angelus.(*Client).call.
func (c *Client) call(ctx context.Context, method string, params, result any) error {
	raw, err := rpc.WithCaller(params, c.caller, c.ref)
	if err != nil {
		return err
	}
	return c.cli.Call(ctx, method, raw, result)
}

// DialClient connects to a figaro agent.
func DialClient(ep transport.Endpoint, onNotify NotifyHandler) (*Client, error) {
	return DialClientWith(ep, onNotify, nil)
}

// DialClientWith is DialClient with connection middleware — the seam the wire
// recorder hangs on. Passing nil is DialClient exactly.
func DialClientWith(ep transport.Endpoint, onNotify NotifyHandler, tap transport.Tap) (*Client, error) {
	conn, err := transport.DialWith(ep, tap)
	if err != nil {
		return nil, err
	}
	cli := jkrpc.NewClient(conn, jkrpc.NotifyFunc(onNotify))
	return &Client{cli: cli, caller: rpc.CallerFromEnv(), ref: rpc.CallerRefFromEnv()}, nil
}

// Qua sends a prompt and returns the newest materialized turn id at accept
// time, an idempotent resume cursor. The reply streams as figaro.aria pages.
// A prompt accepted while a turn is active becomes steering; one accepted
// while idle opens a new turn. The server classifies it at the drain boundary.
func (c *Client) Qua(ctx context.Context, text string, cb *rpc.FormInput) (int, bool, error) {
	var resp rpc.QuaResponse
	err := c.call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: text, Form: cb}, &resp)
	return resp.Cursor, resp.Active, err
}

// Read pulls one aria.Page forward from a turn cursor (the catch-up half of the
// figaro.aria stream) — used after version desync or to seed a listener. The
// request's JSON field remains named sinceLT for wire compatibility.
func (c *Client) Read(ctx context.Context, sinceTurn int) (aria.Page, error) {
	var r aria.Page
	err := c.call(ctx, rpc.MethodRead, rpc.ReadRequest{SinceLT: sinceTurn}, &r)
	return r, err
}

// ReadBefore pages backward from an anchor — the other direction of the
// same cut, for a pager to walk history. A zero anchor means the tail, and the
// anchor's Node matters: a window whose oldest slice starts mid-turn must ask
// for what precedes THAT NODE, not that turn.
func (c *Client) ReadBefore(ctx context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	var r aria.Page
	req := rpc.ReadRequest{Before: int(at.Turn), BeforeNode: int(at.Node), Limit: budget}
	err := c.call(ctx, rpc.MethodRead, req, &r)
	return r, err
}

// Context returns all messages in the figaro's chat history.
func (c *Client) Context(ctx context.Context) (*rpc.ContextResponse, error) {
	var resp rpc.ContextResponse
	if err := c.call(ctx, rpc.MethodContext, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Interrupt asks the figaro to abort its current turn, keeping whatever is
// queued behind it. The queued messages coalesce into one combined message,
// which is what the aria answers next.
func (c *Client) Interrupt(ctx context.Context) error {
	return c.call(ctx, rpc.MethodInterrupt, rpc.InterruptRequest{}, nil)
}

// Hangup is Interrupt with an explicit disposition for the queue, and the
// queue itself comes back: what survived (keep) or what was dropped (clear).
// A cleared queue is returned VERBATIM — one entry per message as typed — so
// the caller can persist it instead of losing it.
func (c *Client) Hangup(ctx context.Context, disposition rpc.QueueDisposition) (*rpc.InterruptResponse, error) {
	var resp rpc.InterruptResponse
	req := rpc.InterruptRequest{Queue: disposition}
	if err := c.cli.Call(ctx, rpc.MethodInterrupt, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Set applies a form patch directly. No LLM round-trip.
func (c *Client) Set(ctx context.Context, patch rpc.FormPatch, ifVersion uint64) (*rpc.SetResponse, error) {
	return c.SetDressed(ctx, nil, patch, ifVersion)
}

// SetDressed is Set with outfit NAMES beside the patch: the names are folded
// into keys at the daemon's API boundary, under the patch's own, and what
// reaches the writer is data. It is how `state outfit <names>` applies — the
// same call every other dressing surface makes.
func (c *Client) SetDressed(ctx context.Context, outfits []string, patch rpc.FormPatch, ifVersion uint64) (*rpc.SetResponse, error) {
	var resp rpc.SetResponse
	if err := c.call(ctx, rpc.MethodSet, rpc.SetRequest{Outfits: outfits, Patch: patch, IfVersion: ifVersion}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Form returns the agent's current form snapshot.
func (c *Client) Form(ctx context.Context) (*rpc.FormResponse, error) {
	var resp rpc.FormResponse
	if err := c.call(ctx, rpc.MethodForm, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Queued returns the aria's queued messages for DISPLAY: the prompts a human
// would recognise as waiting, with pure form carriers omitted. The
// response's Epoch names the generation those ids belong to.
func (c *Client) Queued(ctx context.Context) (*rpc.QueuedResponse, error) {
	var resp rpc.QueuedResponse
	if err := c.call(ctx, rpc.MethodQueued, rpc.QueuedRequest{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// QueuedAll is the CRUD view: every queued message, carriers included, because
// anything that can be deleted has to be addressable.
func (c *Client) QueuedAll(ctx context.Context) (*rpc.QueuedResponse, error) {
	var resp rpc.QueuedResponse
	req := rpc.QueuedRequest{IncludeCarriers: true}
	if err := c.cli.Call(ctx, rpc.MethodQueued, req, &resp); err != nil {
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

// DeleteQueued asks the aria to drop queued messages. The error return is for
// TRANSPORT failures only; whether each id was actually dropped is in the
// results, one per requested id, and a refusal there is a normal answer.
func (c *Client) DeleteQueued(ctx context.Context, req rpc.QueueDeleteRequest) (*rpc.QueueDeleteResponse, error) {
	var resp rpc.QueueDeleteResponse
	if err := c.cli.Call(ctx, rpc.MethodQueueDelete, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateQueued rewrites one queued message's text. Same split: error is
// transport, the outcome is in the result.
func (c *Client) UpdateQueued(ctx context.Context, req rpc.QueueUpdateRequest) (*rpc.QueueUpdateResponse, error) {
	var resp rpc.QueueUpdateResponse
	if err := c.cli.Call(ctx, rpc.MethodQueueUpdate, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Study subscribes the aria to an unbound form ("" lists), Drop
// unsubscribes, Cast runs one casting call (see rpc.CastRequest).
func (c *Client) Study(ctx context.Context, formID string) (*rpc.StudyResponse, error) {
	var resp rpc.StudyResponse
	if err := c.call(ctx, rpc.MethodStudy, rpc.StudyRequest{FormID: formID}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Drop(ctx context.Context, formID string) (*rpc.StudyResponse, error) {
	var resp rpc.StudyResponse
	if err := c.call(ctx, rpc.MethodDrop, rpc.StudyRequest{FormID: formID}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Cast(ctx context.Context, req rpc.CastRequest) (*rpc.CastResponse, error) {
	var resp rpc.CastResponse
	if err := c.call(ctx, rpc.MethodCast, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
