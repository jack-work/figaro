// Package sdk is figaro's client: it speaks the api/ contract over a
// transport and knows nothing of the daemon's internals.
package sdk

import (
	"context"
	"fmt"
	"os"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/jkrpc"
)

// Angelus is a typed JSON-RPC client for the angelus supervisor.
type Angelus struct {
	cli *jkrpc.Client
	// caller is the aria this process belongs to, captured at dial time so one
	// connection cannot change identity mid-stream. Empty for a human CLI.
	caller string
	// ref is the asserted caller reference: attribution only, never a credential.
	ref *rpc.CallerRef
}

// call is the single point every request passes through, so the caller
// identity cannot be forgotten on a new method.
func (c *Angelus) call(ctx context.Context, method string, params, result any) error {
	raw, err := rpc.WithCaller(params, c.caller, c.ref)
	if err != nil {
		return err
	}
	return c.cli.Call(ctx, method, raw, result)
}

func DialAngelus(ep transport.Endpoint) (*Angelus, error) {
	conn, err := transport.Dial(ep)
	if err != nil {
		return nil, err
	}
	return &Angelus{cli: jkrpc.NewClient(conn, nil), caller: rpc.CallerFromEnv(), ref: rpc.CallerRefFromEnv()}, nil
}

func callerCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// Status reports the daemon's uptime, population and build.
func (c *Angelus) Status(ctx context.Context) (*rpc.StatusResponse, error) {
	var resp rpc.StatusResponse
	err := c.call(ctx, rpc.MethodStatus, struct{}{}, &resp)
	return &resp, err
}

// MemCollect forces a garbage collection in the daemon and reports the
// footprint after it. Stops the world; never poll it.
func (c *Angelus) MemCollect(ctx context.Context) (*rpc.StatusResponse, error) {
	var resp rpc.StatusResponse
	err := c.call(ctx, rpc.MethodMemCollect, struct{}{}, &resp)
	return &resp, err
}

// ProviderLedger reads the daemon's recent and in-flight provider round-trips.
func (c *Angelus) ProviderLedger(ctx context.Context, aria string, limit int) (*rpc.ProviderLedgerResponse, error) {
	var resp rpc.ProviderLedgerResponse
	err := c.call(ctx, rpc.MethodProviderLedger, rpc.ProviderLedgerRequest{Aria: aria, Limit: limit}, &resp)
	return &resp, err
}

// Create starts a new figaro; the patch is folded on top of the default outfit.
func (c *Angelus) Create(ctx context.Context, outfits []string, patch *rpc.FormPatch) (*rpc.CreateResponse, error) {
	var resp rpc.CreateResponse
	err := c.call(ctx, rpc.MethodCreate, rpc.CreateRequest{Outfits: outfits, Patch: patch, Cwd: callerCwd()}, &resp)
	return &resp, err
}

// FormCreate mints an unbound form. Parent "" forks the null root; a form id
// duplicates that form. The response's endpoint is live before the call returns.
func (c *Angelus) FormCreate(ctx context.Context, parent string, outfits []string, patch *rpc.FormPatch) (*rpc.FormCreateResponse, error) {
	var resp rpc.FormCreateResponse
	err := c.call(ctx, rpc.MethodFormCreate, rpc.FormCreateRequest{Parent: parent, Outfits: outfits, Patch: patch}, &resp)
	return &resp, err
}

// FormBind births a dormant figaro from a form (or "null").
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

func (c *Angelus) Outfits(ctx context.Context, spec string) (*rpc.OutfitsResponse, error) {
	var resp rpc.OutfitsResponse
	err := c.call(ctx, rpc.MethodOutfits, rpc.OutfitsRequest{Spec: spec}, &resp)
	return &resp, err
}

// Configure patches the server's config.
func (c *Angelus) Configure(ctx context.Context, req rpc.ConfigureRequest) (*rpc.ConfigureResponse, error) {
	var resp rpc.ConfigureResponse
	err := c.call(ctx, rpc.MethodConfigure, req, &resp)
	return &resp, err
}

// GC collects outfit stumps nothing is using; DryRun reports without removing.
func (c *Angelus) GC(ctx context.Context, dryRun bool) (*rpc.GCResponse, error) {
	var resp rpc.GCResponse
	err := c.call(ctx, rpc.MethodGC, rpc.GCRequest{DryRun: dryRun}, &resp)
	return &resp, err
}

// Fork branches a conversation. Zero forks at the head; atTurn replaces that
// turn, atLT forks at that logical time exactly. They are distinct wire fields.
func (c *Angelus) Fork(ctx context.Context, figaroID string, atTurn, atLT uint64, outfits []string, patch *rpc.FormPatch) (*rpc.ForkResponse, error) {
	var resp rpc.ForkResponse
	err := c.call(ctx, rpc.MethodFork,
		rpc.ForkRequest{FigaroID: figaroID, AtTurn: atTurn, AtLT: atLT, Outfits: outfits, Patch: patch}, &resp)
	return &resp, err
}

// Normalize forces deferred topology work to run now. Blocks.
func (c *Angelus) Normalize(ctx context.Context, segments bool) (*rpc.NormalizeResponse, error) {
	var resp rpc.NormalizeResponse
	err := c.cli.Call(ctx, rpc.MethodNormalize, rpc.NormalizeRequest{Segments: segments}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Promote climbs a trunk up `levels` stump-bounded levels; <= 0 means one.
func (c *Angelus) Promote(ctx context.Context, figaroID string, levels int) (*rpc.PromoteResponse, error) {
	var resp rpc.PromoteResponse
	err := c.call(ctx, rpc.MethodPromote, rpc.PromoteRequest{FigaroID: figaroID, Levels: levels}, &resp)
	return &resp, err
}

// Import restores an exported aria as a new conversation.
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

// ListIDs returns the aria list with only ids populated.
func (c *Angelus) ListIDs(ctx context.Context) (*rpc.ListResponse, error) {
	var resp rpc.ListResponse
	if err := c.call(ctx, rpc.MethodList, rpc.ListRequest{IDsOnly: true}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListGlobal includes the ceremonial anchors (null genesis trunk, versioned
// outfits), each with Kind/Parent set.
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

func (c *Angelus) SaveBindings(ctx context.Context) (*rpc.SaveBindingsResponse, error) {
	var resp rpc.SaveBindingsResponse
	if err := c.call(ctx, rpc.MethodSaveBindings, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Read fetches one window of an aria's composed history.
func (c *Angelus) Read(ctx context.Context, req rpc.ReadRequest) (aria.Page, error) {
	var page aria.Page
	if err := c.call(ctx, rpc.MethodRead, req, &page); err != nil {
		return aria.Page{}, err
	}
	return page, nil
}

// IR fetches one page of IR entries. The angelus caps a page regardless of
// limit and reports where the rest starts in NextFrom.
func (c *Angelus) IR(ctx context.Context, figaroID string, from uint64, limit int) (*rpc.IRResponse, error) {
	return c.IRBefore(ctx, figaroID, from, 0, limit)
}

// IRAll reads every IR entry from LT `from` to the end, paging past the cap.
func (c *Angelus) IRAll(ctx context.Context, figaroID string, from uint64) (*rpc.IRResponse, error) {
	out := &rpc.IRResponse{}
	for {
		page, err := c.IR(ctx, figaroID, from, 0)
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, page.Entries...)
		out.Total = page.Total
		if page.NextFrom == 0 || len(page.Entries) == 0 {
			return out, nil
		}
		if page.NextFrom <= from {
			return nil, fmt.Errorf("aria.read: next_from %d did not advance past %d", page.NextFrom, from)
		}
		from = page.NextFrom
	}
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

// Lineage asks where two arias part company. Against may be empty, which asks
// only for the chain.
func (c *Angelus) Lineage(ctx context.Context, id, against string) (*rpc.LineageResponse, error) {
	var out rpc.LineageResponse
	req := rpc.LineageRequest{FigaroID: id, Against: against}
	if err := c.cli.Call(ctx, rpc.MethodLineage, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Angelus) Decide(ctx context.Context, req rpc.DecideRequest) (*rpc.DecideResponse, error) {
	var resp rpc.DecideResponse
	if err := c.call(ctx, rpc.MethodDecide, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
