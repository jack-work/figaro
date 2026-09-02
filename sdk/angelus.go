// Package sdk is figaro's client. It speaks the api/ contract over a
// transport and knows nothing else: no store, no registry, no daemon
// internals. Everything here is importable by anything -- that is the point
// of it being outside internal/, and api/rpc/contract_test.go is what keeps
// the contract underneath it importable too.
package sdk

import (
	"context"
	"os"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/jkrpc"
)

// Client is a typed JSON-RPC client for talking to the angelus supervisor.
// agent:
// this should probably be called ......... idk not cli, it could be
// an sdk.
type Angelus struct {
	cli *jkrpc.Client
	// caller is the aria this process belongs to, presented on every call as
	// the x-internal-figaro-id params field. Captured at dial time rather than
	// read per call so one connection cannot change identity mid-stream.
	// Empty for a human-driven CLI, which then puts nothing extra on the wire.
	caller string
	// ref is the ASSERTED caller reference (the duke placeholder, or an
	// explicit FIGARO_CALLER label). Attribution only, never a credential.
	ref *rpc.CallerRef
}

// call is the single point every request passes through, so the caller
// identity cannot be forgotten on a new method. It pre-marshals params (jkrpc
// marshals whatever it is given, and json.RawMessage passes through verbatim),
// splices the identity in, and hands the bytes on.
func (c *Angelus) call(ctx context.Context, method string, params, result any) error {
	raw, err := rpc.WithCaller(params, c.caller, c.ref)
	if err != nil {
		return err
	}
	return c.cli.Call(ctx, method, raw, result)
}

// DialClient connects to the angelus at the given endpoint.
func DialAngelus(ep transport.Endpoint) (*Angelus, error) {
	conn, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	return &Angelus{cli: jkrpc.NewClient(conn, nil), caller: rpc.CallerFromEnv(), ref: rpc.CallerRefFromEnv()}, nil
}

// callerCwd is the client process's working directory.
func callerCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// Status reports the running daemon's uptime, population and BUILD. The build
// is what lets a CLI refuse to speak to a daemon from another revision.
func (c *Angelus) Status(ctx context.Context) (*rpc.StatusResponse, error) {
	var resp rpc.StatusResponse
	err := c.call(ctx, rpc.MethodStatus, struct{}{}, &resp)
	return &resp, err
}

// MemCollect forces a garbage collection in the daemon and reports the
// footprint after it. Stops the world: a deliberate call, never a poll.
func (c *Angelus) MemCollect(ctx context.Context) (*rpc.StatusResponse, error) {
	var resp rpc.StatusResponse
	err := c.call(ctx, rpc.MethodMemCollect, struct{}{}, &resp)
	return &resp, err
}

// ProviderLedger reads the daemon's recent provider round-trips: what the
// provider was asked, what it answered, and what is still in flight.
func (c *Angelus) ProviderLedger(ctx context.Context, aria string, limit int) (*rpc.ProviderLedgerResponse, error) {
	var resp rpc.ProviderLedgerResponse
	err := c.call(ctx, rpc.MethodProviderLedger, rpc.ProviderLedgerRequest{Aria: aria, Limit: limit}, &resp)
	return &resp, err
}

// Create starts a new figaro from a patch. An empty patch means the configured
// default_outfit; a patch is folded on top of it.
func (c *Angelus) Create(ctx context.Context, outfits []string, patch *rpc.FormPatch) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.call(ctx, rpc.MethodCreate, rpc.CreateRequest{Outfits: outfits, Patch: patch, Cwd: callerCwd()}, &resp)
	return &resp, err
}

// FormCreate mints an unbound form from a birth patch. Parent "" forks the
// null root; a form id duplicates that form. The response's endpoint is
// live before the call returns.
func (c *Angelus) FormCreate(ctx context.Context, parent string, outfits []string, patch *rpc.FormPatch) (*rpc.FormCreateResponse, error) {
	var resp rpc.FormCreateResponse
	err := c.call(ctx, rpc.MethodFormCreate, rpc.FormCreateRequest{Parent: parent, Outfits: outfits, Patch: patch}, &resp)
	return &resp, err
}

// FormBind births a dormant figaro from a form (or "null"). The optional
// patch is the -O dressing.
func (c *Angelus) FormBind(ctx context.Context, parent string, outfits []string, patch *rpc.FormPatch) (*rpc.FormBindResponse, error) {
	var resp rpc.FormBindResponse
	err := c.call(ctx, rpc.MethodFormBind, rpc.FormBindRequest{Parent: parent, Outfits: outfits, Patch: patch, Cwd: callerCwd()}, &resp)
	return &resp, err
}

// OutfitReload flags the default form for recomputation on the next new.
func (c *Angelus) OutfitReload(ctx context.Context) (*rpc.OutfitReloadResponse, error) {
	var resp rpc.OutfitReloadResponse
	err := c.call(ctx, rpc.MethodOutfitReload, struct{}{}, &resp)
	return &resp, err
}

// Outfits asks what outfits exist and how a spec composes.
func (c *Angelus) Outfits(ctx context.Context, spec string) (*rpc.OutfitsResponse, error) {
	var resp rpc.OutfitsResponse
	err := c.call(ctx, rpc.MethodOutfits, rpc.OutfitsRequest{Spec: spec}, &resp)
	return &resp, err
}

// Configure patches the server's config: the seam the first-run wizard drives.
func (c *Angelus) Configure(ctx context.Context, req rpc.ConfigureRequest) (*rpc.ConfigureResponse, error) {
	var resp rpc.ConfigureResponse
	err := c.call(ctx, rpc.MethodConfigure, req, &resp)
	return &resp, err
}

// GC collects outfit stumps nothing is using. DryRun reports without removing.
func (c *Angelus) GC(ctx context.Context, dryRun bool) (*rpc.GCResponse, error) {
	var resp rpc.GCResponse
	err := c.call(ctx, rpc.MethodGC, rpc.GCRequest{DryRun: dryRun}, &resp)
	return &resp, err
}

// Fork branches a conversation: the node freezes and both children get
// fresh system-minted ids. A zero request forks at the head; atTurn
// REPLACES that turn (the server maps it to an LT) and atLT forks at that
// logical time exactly. They are separate parameters, and separate wire
// fields, because passing one where the other belonged is the defect this
// signature exists to prevent.
func (c *Angelus) Fork(ctx context.Context, figaroID string, atTurn, atLT uint64, outfits []string, patch *rpc.FormPatch) (*rpc.ForkResponse, error) {
	var resp rpc.ForkResponse
	err := c.call(ctx, rpc.MethodFork,
		rpc.ForkRequest{FigaroID: figaroID, AtTurn: atTurn, AtLT: atLT, Outfits: outfits, Patch: patch}, &resp)
	return &resp, err
}

// Promote climbs a conversation trunk up `levels` stump-bounded levels (it
// absorbs its parent trunk's run). levels <= 0 means one level.
// Normalize forces deferred topology work to run now. Blocking by design.
func (c *Angelus) Normalize(ctx context.Context, segments bool) (*rpc.NormalizeResponse, error) {
	var resp rpc.NormalizeResponse
	err := c.cli.Call(ctx, rpc.MethodNormalize, rpc.NormalizeRequest{Segments: segments}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Angelus) Promote(ctx context.Context, figaroID string, levels int) (*rpc.PromoteResponse, error) {
	var resp rpc.PromoteResponse
	err := c.call(ctx, rpc.MethodPromote, rpc.PromoteRequest{FigaroID: figaroID, Levels: levels}, &resp)
	return &resp, err
}

// Import restores an exported aria as a new conversation. See the handler.
func (c *Angelus) Import(ctx context.Context, req rpc.ImportRequest) (*rpc.ImportResponse, error) {
	var resp rpc.ImportResponse
	err := c.call(ctx, rpc.MethodImport, req, &resp)
	return &resp, err
}

func (c *Angelus) Kill(ctx context.Context, figaroID string, recursive bool) error {
	return c.call(ctx, rpc.MethodKill, rpc.KillRequest{FigaroID: figaroID, Recursive: recursive}, nil)
}

func (c *Angelus) List(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.call(ctx, rpc.MethodList, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListIDs returns the aria list with only ids populated (skips the expensive
// per-aria form/tree fills). For completion and other id-only callers.
func (c *Angelus) ListIDs(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.call(ctx, rpc.MethodList, rpc.ListRequest{IDsOnly: true}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListGlobal returns the aria list including the ceremonial anchors (the null
// genesis trunk + every versioned outfit), each with Kind/Parent set: for the
// `ls -g` hierarchy and the `--json` escape hatch.
func (c *Angelus) ListGlobal(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.call(ctx, rpc.MethodList, rpc.ListRequest{Global: true}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Attach restores a dormant aria without binding a pid.
func (c *Angelus) Attach(ctx context.Context, figaroID string) (*rpc.AttachResponse, error) {
	var resp rpc.AttachResponse
	if err := c.call(ctx, rpc.MethodAttach, rpc.AttachRequest{FigaroID: figaroID}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Angelus) Bind(ctx context.Context, pid int, figaroID string, atMainLT uint64) error {
	return c.call(ctx, rpc.MethodBind, rpc.BindRequest{PID: pid, FigaroID: figaroID, AtMainLT: atMainLT}, nil)
}

func (c *Angelus) Resolve(ctx context.Context, pid int) (*rpc.ResolveResponse, error) {
	var resp rpc.ResolveResponse
	if err := c.call(ctx, rpc.MethodResolve, rpc.ResolveRequest{PID: pid}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Angelus) Unbind(ctx context.Context, pid int) error {
	return c.call(ctx, rpc.MethodUnbind, rpc.UnbindRequest{PID: pid}, nil)
}

// SaveBindings persists PID->figaro bindings to disk.
func (c *Angelus) SaveBindings(ctx context.Context) (*rpc.SaveBindingsResponse, error) {
	var resp rpc.SaveBindingsResponse
	if err := c.call(ctx, rpc.MethodSaveBindings, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Read fetches ONE window of an aria's COMPOSED history: turns, built
// by whoever owns them (the live agent, or the reader over the store) and
// held in the process-wide composed cache. A client renders these; it does
// not build turns out of raw IR.
func (c *Angelus) Read(ctx context.Context, req rpc.ReadRequest) (aria.Page, error) {
	var page aria.Page
	if err := c.call(ctx, rpc.MethodRead, req, &page); err != nil {
		return aria.Page{}, err
	}
	return page, nil
}

// IR fetches IR entries for an aria through the angelus's
// shared LogCache.
func (c *Angelus) IR(ctx context.Context, figaroID string, from uint64, limit int) (*rpc.IRResponse, error) {
	return c.IRBefore(ctx, figaroID, from, 0, limit)
}

func (c *Angelus) IRBefore(ctx context.Context, figaroID string, from, before uint64, limit int) (*rpc.IRResponse, error) {
	var resp rpc.IRResponse
	err := c.call(ctx, rpc.MethodIR, rpc.IRRequest{
		FigaroID: figaroID, From: from, Before: before, Limit: limit,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Angelus) Close() error {
	return c.cli.Close()
}
