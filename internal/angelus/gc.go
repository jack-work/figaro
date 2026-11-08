package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/gc"
	"github.com/jack-work/figaro/internal/store"
)

var errRoleIndexLoading = errors.New("gc: the role index is still loading; plan again in a moment")

type gcWorld struct{ h *handlers }

func (h *handlers) collector() *gc.Collector {
	h.gcOnce.Do(func() { h.gcCollector = gc.New(gcWorld{h: h}) })
	return h.gcCollector
}

func (w gcWorld) Now() time.Time { return time.Now() }

func (w gcWorld) Rows() []gc.Row {
	b := w.h.angelus.Backend
	nodes := b.Conversations()
	out := make([]gc.Row, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind != conversationKind {
			continue
		}
		meta, _ := b.Meta(n.ID)
		if meta != nil && meta.ReapedAtMS > 0 {
			continue
		}
		r := gc.Row{ID: n.ID, Parent: presented(n), Outfit: n.Outfit, LastUseMS: w.h.angelus.lastUse(n.ID, meta)}
		if meta != nil {
			r.Mantra, r.Cwd, r.CreatedMS, r.Messages = meta.Mantra, meta.Cwd, meta.CreatedAtMS, meta.MessageCount
		}
		out = append(out, r)
	}
	return out
}

func presented(n store.NodeView) string {
	if n.Present != "" {
		return n.Present
	}
	return n.Parent
}

func (w gcWorld) Parent(id string) (string, bool) {
	n, ok := w.h.angelus.Backend.Node(id)
	if !ok {
		return "", false
	}
	return presented(n), true
}

func (w gcWorld) Reaped(id string) bool {
	meta, _ := w.h.angelus.Backend.Meta(id)
	return meta != nil && meta.ReapedAtMS > 0
}

func (w gcWorld) Handle(id string) string {
	_, words := names(w.h.angelus.Backend, id)
	return words
}

func (w gcWorld) Empty(id string) bool { return w.h.angelus.isEmpty(id) }

func (w gcWorld) Box(id string) string {
	snap, err := w.h.angelus.Backend.FormState(id)
	if err != nil {
		return ""
	}
	return w.h.heldBox(snap)
}

func (w gcWorld) DeleteSet(id string) []string {
	if k, ok := w.h.angelus.Backend.(interface{ DeleteSet(string) []string }); ok {
		return k.DeleteSet(id)
	}
	return []string{id}
}

func (w gcWorld) Hold(set []string, root string) (string, string, error) {
	return w.h.holdOf(set, root, time.Now())
}

func (w gcWorld) Bytes(id string) int64 {
	if b, ok := w.h.angelus.Backend.(interface{ OwnBytes(string) int64 }); ok {
		return b.OwnBytes(id)
	}
	return 0
}

func (w gcWorld) Stumps() []gc.Stump {
	nodes := w.h.angelus.Backend.Nodes()
	children := map[string]int{}
	for _, n := range nodes {
		if n.Parent != "" {
			children[n.Parent]++
		}
	}
	var out []gc.Stump
	for _, n := range nodes {
		if n.Kind == outfitKind {
			out = append(out, gc.Stump{ID: n.ID, Outfit: n.Outfit, Version: n.Version, Children: children[n.ID]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (w gcWorld) Collect(stump string) error { return w.h.angelus.Backend.CollectStump(stump) }

func (w gcWorld) Reap(root string, expect []string, stamp string) error {
	grace := w.h.killGrace(root, 0)
	if grace <= 0 {
		return errors.New("gc reaps and never deletes, and the kill grace here is 0")
	}
	_, ok, err := w.h.reapGroup(root, true, time.Now().Add(grace), expect, stamp)
	if errors.Is(err, store.ErrDeleteSetChanged) {
		return fmt.Errorf("%w: %v", gc.ErrSetChanged, err)
	}
	if err == nil && !ok {
		return fmt.Errorf("%s is not a conversation this store reaps", root)
	}
	return err
}

func (h *handlers) killGrace(id string, asked time.Duration) time.Duration {
	if box, ok := h.config.BoxPurgeGrace(h.angelus.sandboxOf(id)); ok {
		if asked <= 0 || asked > box {
			return box
		}
		return asked
	}
	if asked > 0 {
		return asked
	}
	return h.config.KillGrace()
}

func (a *Angelus) lastUse(id string, meta *store.AriaMeta) int64 {
	var access int64
	if t, ok := a.Backend.(toucher); ok {
		access = t.LastAccess(id)
	} else if meta != nil {
		access = meta.LastAccessMS
	}
	return store.LastUse(access, a.Backend.LastTS(id))
}

func (a *Angelus) keptAlive(id string, def time.Duration, now time.Time) bool {
	meta, _ := a.Backend.Meta(id)
	ka := store.KeepaliveFor(meta, def)
	return ka > 0 && a.lastUse(id, meta)+ka.Milliseconds() > now.UnixMilli()
}

func (h *handlers) holdOf(set []string, root string, now time.Time) (string, string, error) {
	a := h.angelus
	roles, ready := a.roleTargets()
	if !ready {
		return "", "", errRoleIndexLoading
	}
	if node, why := a.liveIn(set, a.Registry.BoundPIDsByFigaro(), roles); why != "" {
		return node, why, nil
	}
	def := a.keepalive()
	for _, id := range set {
		if hb := a.Hubs.get(id); hb != nil && hb.Attached() > 0 {
			return id, "a pane is attending", nil
		}
		if a.Sessions != nil && a.Sessions.Running(id) > 0 {
			return id, "a background session runs", nil
		}
		if h.holdsMove(id) != nil {
			return id, "it is moving to another node", nil
		}
		if a.keptAlive(id, def, now) {
			return id, "accessed within its keepalive", nil
		}
		if snap, err := a.Backend.FormState(id); err == nil && store.KeepSet(snap) {
			return id, "system.keep is set", nil
		}
	}
	if node := a.otherSandboxIn(set, root); node != "" {
		return node, "it runs in another sandbox", nil
	}
	return "", "", nil
}

func (h *handlers) gcCaller(ctx context.Context) (gc.Caller, error) {
	principal := authz.PrincipalText(authz.PrincipalOf(ctx))
	if box, ok := callerBox(ctx); ok {
		return gc.Caller{Principal: principal, Box: box, MayKill: func(id string) error {
			return h.allowed(ctx, rpc.VerbKill, authz.Aria(id))
		}}, nil
	}
	if err := h.allowed(ctx, rpc.VerbAdmin, authz.Daemon); err != nil {
		return gc.Caller{}, fmt.Errorf("gc chooses figaros to kill, so it is the owner's: %w", err)
	}
	return gc.Caller{Principal: principal}, nil
}

func (h *handlers) gcSelector(ctx context.Context, s rpc.GCSelector) (gc.Selector, error) {
	sel := gc.Selector{
		Idle:   time.Duration(s.IdleMS) * time.Millisecond,
		Empty:  s.Empty,
		Older:  time.Duration(s.OlderMS) * time.Millisecond,
		Outfit: s.Outfit,
		Match:  s.Match,
		Box:    s.Box,
	}
	if s.Under == "" {
		return sel, nil
	}
	if node, _ := rpc.SplitNode(s.Under); node != "" && !h.isSelf(node) {
		return gc.Selector{}, fmt.Errorf("gc works on this node only; %s names node %s", s.Under, node)
	}
	id, err := h.canonicalID(ctx, s.Under)
	if err != nil {
		return gc.Selector{}, err
	}
	if node, _ := rpc.SplitNode(id); node != "" {
		return gc.Selector{}, fmt.Errorf("gc works on this node only; %s is on node %s", s.Under, node)
	}
	if h.allowed(ctx, rpc.VerbRead, authz.Aria(id)) != nil {
		return gc.Selector{}, fmt.Errorf("%w: %s", gc.ErrUnknownUnder, s.Under)
	}
	sel.Under = id
	return sel, nil
}

func (h *handlers) gcPlan(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.GCPlanRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	caller, err := h.gcCaller(ctx)
	if err != nil {
		return nil, err
	}
	sel, err := h.gcSelector(ctx, req.Selector)
	if err != nil {
		return nil, err
	}
	p, err := h.collector().Plan(sel, caller)
	if err != nil {
		return nil, err
	}
	resp := rpc.GCPlanResponse{
		Run: p.Run, ExpiresAt: p.Expires().UnixMilli(), GraceMS: h.config.KillGrace().Milliseconds(),
		Candidates: []rpc.GCCandidate{}, Kept: []rpc.GCKept{},
	}
	for _, c := range p.Candidates {
		row := gcRow(c)
		resp.Candidates = append(resp.Candidates, row)
		resp.Nodes += len(c.Set)
		resp.Bytes += c.Bytes
	}
	for _, k := range p.Kept {
		resp.Kept = append(resp.Kept, rpc.GCKept{ID: k.ID, Handle: k.Handle, Mantra: k.Mantra, HeldBy: k.By, Why: k.Why})
	}
	for _, s := range p.Stumps {
		resp.Stumps = append(resp.Stumps, rpc.GCStump{ID: s.ID, Outfit: s.Outfit, Version: s.Version, Children: s.Children, Collected: s.Children == 0})
	}
	return resp, nil
}

func gcRow(c gc.Candidate) rpc.GCCandidate {
	row := rpc.GCCandidate{ID: c.ID, Handle: c.Handle, Mantra: c.Mantra, IdleMS: c.Idle.Milliseconds(), Messages: c.Messages, Bytes: c.Bytes}
	for _, n := range c.Set {
		if n != c.ID {
			row.Takes = append(row.Takes, n)
		}
	}
	return row
}

func (h *handlers) gcApply(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.GCApplyRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	caller, err := h.gcCaller(ctx)
	if err != nil {
		return nil, err
	}
	c := h.collector()
	p, err := c.Take(req.Run, caller.Principal)
	if err != nil {
		return nil, err
	}
	p.Caller = caller
	res, err := c.Apply(p)
	if err != nil {
		return nil, err
	}
	resp := rpc.GCApplyResponse{Run: res.Run, Reaped: []rpc.GCCandidate{}, Dropped: []rpc.GCDropped{}, Collected: len(res.Collected)}
	for _, r := range res.Reaped {
		resp.Reaped = append(resp.Reaped, gcRow(r))
		resp.Nodes += len(r.Set)
		resp.Bytes += r.Bytes
	}
	for _, d := range res.Dropped {
		resp.Dropped = append(resp.Dropped, rpc.GCDropped{ID: d.ID, Handle: d.Handle, Why: d.Why})
	}
	for _, d := range res.Failed {
		resp.Failed = append(resp.Failed, rpc.GCDropped{ID: d.ID, Handle: d.Handle, Why: d.Why})
	}
	slog.Info("gc applied", "run", res.Run, "reaped", len(res.Reaped), "nodes", resp.Nodes,
		"dropped", len(res.Dropped), "failed", len(res.Failed), "stumps", len(res.Collected))
	return resp, nil
}

func (h *handlers) gcUndo(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.GCUndoRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Run == "" {
		return nil, errors.New("gc undo: no run named")
	}
	stamp := "gc:" + req.Run
	var groups []reapedGroup
	for _, g := range h.angelus.reapedGroups() {
		if g.by == stamp {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("gc undo: nothing reaped by run %s is left to restore", req.Run)
	}
	for _, g := range groups {
		if err := h.mayRestore(ctx, g); err != nil {
			return nil, fmt.Errorf("gc undo %s: %w", req.Run, err)
		}
	}
	resp := rpc.GCUndoResponse{Run: req.Run, Restored: []string{}}
	for _, g := range groups {
		got, err := h.restoreGroup(ctx, g, false)
		if err != nil {
			return resp, fmt.Errorf("gc undo %s: restored %d of %d groups: %w", req.Run, len(resp.Restored), len(groups), err)
		}
		resp.Restored = append(resp.Restored, got.Root)
		resp.Nodes += len(got.Restored)
	}
	slog.Info("gc undone", "run", req.Run, "groups", len(resp.Restored), "nodes", resp.Nodes)
	return resp, nil
}
