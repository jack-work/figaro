package sdk

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/jkrpc"
)

// NotifyHandler receives server-pushed notifications in wire order.
type NotifyHandler func(method string, params json.RawMessage)

// Aria is a typed JSON-RPC client for a figaro agent socket.
type Aria struct {
	cli *jkrpc.Client
	// caller is presented on this hop independently of the angelus hop.
	caller string
	// ref is the asserted caller reference: attribution only, never a credential.
	ref *rpc.CallerRef
}

func (c *Aria) call(ctx context.Context, method string, params, result any) error {
	raw, err := rpc.WithCaller(params, c.caller, c.ref)
	if err != nil {
		return err
	}
	return c.cli.Call(ctx, method, raw, result)
}

func DialAria(ep transport.Endpoint, onNotify NotifyHandler) (*Aria, error) {
	return DialAriaWith(ep, onNotify, nil)
}

// DialAriaWith is DialAria with connection middleware; nil tap is DialAria exactly.
func DialAriaWith(ep transport.Endpoint, onNotify NotifyHandler, tap transport.Tap) (*Aria, error) {
	conn, err := transport.DialWith(ep, tap)
	if err != nil {
		return nil, err
	}
	cli := jkrpc.NewClient(conn, jkrpc.NotifyFunc(onNotify))
	return &Aria{cli: cli, caller: rpc.CallerFromEnv(), ref: rpc.CallerRefFromEnv()}, nil
}

// Qua sends a prompt and returns the newest materialized turn id at accept
// time. A prompt accepted while a turn is active becomes steering; one
// accepted while idle opens a new turn.
func (c *Aria) Qua(ctx context.Context, text string, cb *rpc.FormInput) (int, bool, error) {
	var resp rpc.QuaResponse
	err := c.call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: text, Form: cb}, &resp)
	return resp.Cursor, resp.Active, err
}

// Read pulls one aria.Page forward from a coordinate. A zero anchor is the head.
func (c *Aria) Read(ctx context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	var r aria.Page
	err := c.call(ctx, rpc.MethodRead, rpc.ReadRequest{At: at, Limit: budget}, &r)
	return r, err
}

// ReadBefore pages backward from a coordinate, excluding it. A zero anchor is
// the live tail; the anchor's Node matters.
func (c *Aria) ReadBefore(ctx context.Context, at aria.Anchor, budget int) (aria.Page, error) {
	return c.ReadBeforeFloor(ctx, at, aria.Anchor{}, budget)
}

// ReadBeforeFloor is ReadBefore stopped from below: no part below floor
// (inclusive), More.Before instead.
func (c *Aria) ReadBeforeFloor(ctx context.Context, at, floor aria.Anchor, budget int) (aria.Page, error) {
	var r aria.Page
	req := rpc.ReadRequest{At: at, Backward: true, Limit: budget, Floor: floor}
	err := c.call(ctx, rpc.MethodRead, req, &r)
	return r, err
}

// metricsProbe is past the last turn any aria can hold, so a forward read
// from it carries metrics and nothing else.
var metricsProbe = aria.Anchor{Turn: ^uint64(0)}

// Metrics asks for the status-bar figures alone; a budget is bytes, not messages.
func (c *Aria) Metrics(ctx context.Context) (*aria.Metrics, error) {
	var r aria.Page
	req := rpc.ReadRequest{At: metricsProbe, Limit: 1}
	if err := c.call(ctx, rpc.MethodRead, req, &r); err != nil {
		return nil, err
	}
	return r.Metrics, nil
}

func (c *Aria) Context(ctx context.Context) (*rpc.ContextResponse, error) {
	var resp rpc.ContextResponse
	if err := c.call(ctx, rpc.MethodContext, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Interrupt aborts the current turn, keeping the queue; queued messages
// coalesce into one.
func (c *Aria) Interrupt(ctx context.Context) error {
	return c.call(ctx, rpc.MethodInterrupt, rpc.InterruptRequest{}, nil)
}

// Hangup is Interrupt with an explicit queue disposition; the queue comes back
// verbatim, one entry per message as typed.
func (c *Aria) Hangup(ctx context.Context, disposition rpc.QueueDisposition) (*rpc.InterruptResponse, error) {
	var resp rpc.InterruptResponse
	req := rpc.InterruptRequest{Queue: disposition}
	if err := c.cli.Call(ctx, rpc.MethodInterrupt, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Set applies a form patch directly, with no LLM round-trip.
func (c *Aria) Set(ctx context.Context, patch rpc.FormPatch, ifVersion uint64) (*rpc.SetResponse, error) {
	return c.SetDressed(ctx, nil, patch, ifVersion)
}

// SetDressed is Set with outfit names folded into keys under the patch's own.
func (c *Aria) SetDressed(ctx context.Context, outfits []string, patch rpc.FormPatch, ifVersion uint64) (*rpc.SetResponse, error) {
	return c.setWith(ctx, rpc.SetRequest{Outfits: outfits, Patch: patch, IfVersion: ifVersion})
}

// SetAsserting refuses a removal of a key that is not there.
func (c *Aria) SetAsserting(ctx context.Context, patch rpc.FormPatch, ifVersion uint64) (*rpc.SetResponse, error) {
	return c.setWith(ctx, rpc.SetRequest{Patch: patch, IfVersion: ifVersion, Assert: true})
}

// SetWaiting returns the writer's verdict rather than a queued ack; it blocks
// until the next round boundary, bounded only by ctx.
func (c *Aria) SetWaiting(ctx context.Context, patch rpc.FormPatch, ifVersion uint64, assert bool) (*rpc.SetResponse, error) {
	return c.setWith(ctx, rpc.SetRequest{Patch: patch, IfVersion: ifVersion, Assert: assert, Wait: true})
}

func (c *Aria) setWith(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	var resp rpc.SetResponse
	if err := c.call(ctx, rpc.MethodSet, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Form returns the host's own form snapshot.
func (c *Aria) Form(ctx context.Context) (*rpc.FormResponse, error) {
	return c.FormOf(ctx, "")
}

// FormOf returns one named form: "" and "state" are the host's own; "queue"
// and "runtime" are its intrinsic forms.
func (c *Aria) FormOf(ctx context.Context, intrinsic string) (*rpc.FormResponse, error) {
	var resp rpc.FormResponse
	var params any
	if intrinsic != "" {
		params = rpc.FormRequest{Intrinsic: intrinsic}
	}
	if err := c.call(ctx, rpc.MethodForm, params, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Queued returns the queued messages for display, pure form carriers omitted.
// Epoch names the generation the ids belong to.
func (c *Aria) Queued(ctx context.Context) (*rpc.QueuedResponse, error) {
	var resp rpc.QueuedResponse
	if err := c.call(ctx, rpc.MethodQueued, rpc.QueuedRequest{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// QueuedAll is every queued message, carriers included.
func (c *Aria) QueuedAll(ctx context.Context) (*rpc.QueuedResponse, error) {
	var resp rpc.QueuedResponse
	req := rpc.QueuedRequest{IncludeCarriers: true}
	if err := c.cli.Call(ctx, rpc.MethodQueued, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Aria) Close() error {
	return c.cli.Close()
}

// Done is closed when the connection dies.
func (c *Aria) Done() <-chan struct{} {
	return c.cli.Done()
}

// DeleteQueued drops queued messages. The error is transport only; per-id
// outcomes are in the results.
func (c *Aria) DeleteQueued(ctx context.Context, req rpc.QueueDeleteRequest) (*rpc.QueueDeleteResponse, error) {
	var resp rpc.QueueDeleteResponse
	if err := c.cli.Call(ctx, rpc.MethodQueueDelete, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateQueued rewrites one queued message's text; error is transport only.
func (c *Aria) UpdateQueued(ctx context.Context, req rpc.QueueUpdateRequest) (*rpc.QueueUpdateResponse, error) {
	var resp rpc.QueueUpdateResponse
	if err := c.cli.Call(ctx, rpc.MethodQueueUpdate, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Study subscribes the aria to an unbound form ("" lists); Drop unsubscribes;
// Cast runs one casting call.
func (c *Aria) Study(ctx context.Context, formID string) (*rpc.StudyResponse, error) {
	var resp rpc.StudyResponse
	if err := c.call(ctx, rpc.MethodStudy, rpc.StudyRequest{FormID: formID}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Aria) Drop(ctx context.Context, formID string) (*rpc.StudyResponse, error) {
	var resp rpc.StudyResponse
	if err := c.call(ctx, rpc.MethodDrop, rpc.StudyRequest{FormID: formID}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Aria) Cast(ctx context.Context, req rpc.CastRequest) (*rpc.CastResponse, error) {
	var resp rpc.CastResponse
	if err := c.call(ctx, rpc.MethodCast, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
