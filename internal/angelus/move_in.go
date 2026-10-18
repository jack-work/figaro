package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/store/xwal"
)

type grafts struct {
	mu   sync.Mutex
	open map[string]*xwal.Graft
}

func (g *grafts) get(mv mover, token string) (*xwal.Graft, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr, ok := g.open[token]; ok {
		return gr, nil
	}
	gr, err := mv.OpenImport(token)
	if err != nil {
		return nil, err
	}
	if g.open == nil {
		g.open = map[string]*xwal.Graft{}
	}
	g.open[token] = gr
	return gr, nil
}

func (g *grafts) put(token string, gr *xwal.Graft) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open == nil {
		g.open = map[string]*xwal.Graft{}
	}
	g.open[token] = gr
}

func (g *grafts) drop(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.open, token)
}

func forwardedCaller(ctx context.Context, method string) (string, string, error) {
	via, ok := peer.Via(ctx)
	if !ok {
		return "", "", fmt.Errorf("%s comes only from a peer node", method)
	}
	p := authz.PrincipalOf(ctx)
	if p.Kind == rpc.PrincipalNode {
		return "", "", fmt.Errorf("%s carries the caller who asked for the move, not a node", method)
	}
	return via, authz.PrincipalText(p), nil
}

func (h *handlers) arrival(ctx context.Context, method, token string) (mover, store.Arrival, error) {
	via, caller, err := forwardedCaller(ctx, method)
	if err != nil {
		return nil, store.Arrival{}, err
	}
	mv, err := h.mover()
	if err != nil {
		return nil, store.Arrival{}, err
	}
	a, ok := mv.Moves().Arrival(token)
	if !ok || a.From != via || a.Caller != caller {
		return nil, store.Arrival{}, fmt.Errorf("%s: no move %s from this caller on node %s", method, token, via)
	}
	return mv, a, nil
}

func (h *handlers) importBegin(ctx context.Context, params json.RawMessage) (any, error) {
	via, caller, err := forwardedCaller(ctx, rpc.MethodImportBegin)
	if err != nil {
		return nil, err
	}
	var req rpc.ImportBeginRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	var comp xwal.Component
	if err := json.Unmarshal(req.Component, &comp); err != nil {
		return nil, fmt.Errorf("%s: component: %w", rpc.MethodImportBegin, err)
	}
	ids := comp.TrunkIDs()
	for _, id := range ids {
		if err := h.allowed(ctx, rpc.VerbUse, authz.Sandbox(req.Sandboxes[id])); err != nil {
			return nil, fmt.Errorf("%s: %s runs in sandbox %q: %w", rpc.MethodImportBegin, id, sandboxName(req.Sandboxes[id]), err)
		}
		if err := h.holdsMove(id); err != nil {
			return nil, err
		}
	}
	mv, err := h.mover()
	if err != nil {
		return nil, err
	}
	g, err := mv.BeginImport(req.Token, comp)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodImportBegin, err)
	}
	if err := mv.Moves().Arrive(store.Arrival{Token: req.Token, From: via, Caller: caller, IDs: ids}); err != nil {
		mv.AbortImport(g)
		return nil, err
	}
	h.grafts.put(req.Token, g)
	return struct{}{}, nil
}

func (h *handlers) importChunk(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.ImportChunkRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if len(req.Data) > rpc.MaxImportChunk {
		return nil, fmt.Errorf("%s: %d bytes, at most %d", rpc.MethodImportChunk, len(req.Data), rpc.MaxImportChunk)
	}
	mv, _, err := h.arrival(ctx, rpc.MethodImportChunk, req.Token)
	if err != nil {
		return nil, err
	}
	g, err := h.grafts.get(mv, req.Token)
	if err != nil {
		return nil, err
	}
	if err := g.Write(req.Node, req.Channel, req.Name, req.Offset, req.Data); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodImportChunk, err)
	}
	return struct{}{}, nil
}

func (h *handlers) importCommit(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.ImportCommitRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	mv, a, err := h.arrival(ctx, rpc.MethodImportCommit, req.Token)
	if err != nil {
		return nil, err
	}
	g, err := h.grafts.get(mv, req.Token)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	if err := mv.CommitImport(g, req.Digest, req.Metas); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodImportCommit, err)
	}
	h.grafts.drop(req.Token)
	h.landed(mv, a)
	slog.Info("move committed", "token", req.Token, "ids", len(a.IDs), "commit", time.Since(start))
	return struct{}{}, nil
}

func (h *handlers) landed(mv mover, a store.Arrival) {
	if err := mv.Moves().Reclaim(a.IDs); err != nil {
		slog.Error("move: old tombstones for returning arias", "token", a.Token, "err", err)
	}
	if err := mv.Moves().Land(a.Token); err != nil {
		slog.Error("move: arrival not recorded as landed", "token", a.Token, "err", err)
	}
	if h.fed != nil {
		for _, id := range a.IDs {
			h.fed.dir.Forget(id)
		}
	}
	slog.Info("arias arrived", "from", a.From, "ids", a.IDs)
}

func (h *handlers) importAbort(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.ImportTokenRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	mv, _, err := h.arrival(ctx, rpc.MethodImportAbort, req.Token)
	if err != nil {
		return nil, err
	}
	return struct{}{}, h.abandon(mv, req.Token)
}

func (h *handlers) abandon(mv mover, token string) error {
	g, err := h.grafts.get(mv, token)
	if err == nil {
		if err := mv.AbortImport(g); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	h.grafts.drop(token)
	return mv.Moves().Abandon(token)
}

func (h *handlers) importStatus(ctx context.Context, params json.RawMessage) (any, error) {
	node, err := asPeerNode(ctx, rpc.MethodImportStatus)
	if err != nil {
		return nil, err
	}
	var req rpc.ImportStatusRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	mv, err := h.mover()
	if err != nil {
		return nil, err
	}
	a, ok := mv.Moves().Arrival(req.Token)
	if !ok || a.From != node {
		return rpc.ImportStatusResponse{State: rpc.ImportUnknown}, nil
	}
	if !a.Landed && h.holdsAll(a.IDs) {
		h.landed(mv, a)
		a.Landed = true
	}
	if a.Landed {
		return rpc.ImportStatusResponse{State: rpc.ImportCommitted}, nil
	}
	if req.Abandon {
		if err := h.abandon(mv, req.Token); err != nil {
			return nil, err
		}
		return rpc.ImportStatusResponse{State: rpc.ImportUnknown}, nil
	}
	return rpc.ImportStatusResponse{State: rpc.ImportStaged}, nil
}

func (h *handlers) holdsAll(ids []string) bool {
	for _, id := range ids {
		if _, ok := h.angelus.Backend.Node(id); !ok {
			return false
		}
	}
	return len(ids) > 0
}

func asPeerNode(ctx context.Context, method string) (string, error) {
	via, ok := peer.Via(ctx)
	p := authz.PrincipalOf(ctx)
	if !ok || p.Kind != rpc.PrincipalNode || p.ID != via {
		return "", fmt.Errorf("%s is answered only to a peer node speaking for itself", method)
	}
	return via, nil
}

func (h *handlers) nodeLearn(ctx context.Context, params json.RawMessage) (any, error) {
	if _, err := asPeerNode(ctx, rpc.MethodNodeLearn); err != nil {
		return nil, err
	}
	var req rpc.NodeLearnRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.fed == nil || h.isSelf(req.Node) {
		return struct{}{}, nil
	}
	if _, ok := h.fed.members.Member(req.Node); !ok {
		return nil, fmt.Errorf("%s: node %s is not a peer of this node", rpc.MethodNodeLearn, req.Node)
	}
	for _, id := range req.IDs {
		if !h.knownID(id) {
			h.fed.dir.Learn(id, req.Node)
		}
	}
	return struct{}{}, nil
}
