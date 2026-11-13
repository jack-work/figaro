package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

func (h *handlers) bindLeasedSandbox(ctx context.Context, id, parent string) error {
	ls := h.angelus.Leases
	kind := h.sandboxOf(id)
	if ls == nil || kind == "" {
		return nil
	}
	loaded, _ := h.settings()
	switch {
	case ls.IsClass(kind):
		row, err := ls.lease(ctx, loaded, kind, id)
		if errors.Is(err, errNoFreeSandbox) {
			return fmt.Errorf("%w; a sandbox returns when the last figaro holding it is killed", err)
		}
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(row.Sandbox)
		if _, err := h.angelus.Backend.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": raw}, nil)); err != nil {
			ls.DropHolder(ctx, loaded, row.Sandbox, id)
			return fmt.Errorf("bind to sandbox %s: %w", row.Sandbox, err)
		}
		return nil
	case ls.IsSandbox(kind):
		if parent != "" && ls.Holds(kind, parent) {
			return ls.AddHolder(kind, id)
		}
		return fmt.Errorf("sandbox %s is leased from a node: a figaro reaches it by naming its class, or by forking a figaro that holds it", kind)
	}
	return nil
}

func (h *handlers) refuseLeasedSandboxName(snap form.Snapshot) error {
	ls := h.angelus.Leases
	if ls == nil {
		return nil
	}
	if s := snap.Lookup("system.sandbox"); s != nil && ls.IsSandbox(*s) {
		return fmt.Errorf("sandbox %s is leased from a node: name its class instead", *s)
	}
	return nil
}

func (h *handlers) checkLeasedSandboxWrite(ctx context.Context, id string, patch form.Patch) error {
	ls := h.angelus.Leases
	if ls == nil || !patchTouches(patch, "system.sandbox") {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	str := func(s form.Snapshot) string {
		if v := s.Lookup("system.sandbox"); v != nil {
			return *v
		}
		return ""
	}
	cur, next := str(snap), str(snap.Apply(patch))
	if cur == next {
		return nil
	}
	if ls.IsSandbox(next) {
		return fmt.Errorf("sandbox %s is leased from a node: a figaro reaches it by naming its class at birth, or by forking a figaro that holds it", next)
	}
	n, ok := h.angelus.Backend.Node(id)
	figaro := ok && n.Kind == store.KindConversation
	if figaro && ls.IsClass(next) {
		return fmt.Errorf("a figaro takes sandbox class %s at birth: fork it with system.sandbox = %s", next, next)
	}
	if !ls.IsSandbox(cur) {
		return nil
	}
	if !h.callerIsOwner(ctx) {
		return fmt.Errorf("%s is bound to leased sandbox %s, and only the owner moves it out", id, cur)
	}
	loaded, _ := h.settings()
	ls.DropHolder(ctx, loaded, cur, id)
	return nil
}

func (h *handlers) mayRunIn(kind, id string) error {
	ls := h.angelus.Leases
	if ls == nil || !ls.IsSandbox(kind) {
		return nil
	}
	if !ls.Holds(kind, id) {
		return fmt.Errorf("%s is not a holder of sandbox %s's lease, so its tools run nowhere; fork a holder, or name the sandbox's class", id, kind)
	}
	loaded, _ := h.settings()
	if !loaded.BoundSandbox(kind) {
		return fmt.Errorf("sandbox %s's lease is not recovered yet: its node agent has not answered since the daemon started", kind)
	}
	return nil
}

func (h *handlers) leaveLeasedSandbox(id, kind string) {
	ls := h.angelus.Leases
	if ls == nil || !ls.IsSandbox(kind) {
		return
	}
	loaded, _ := h.settings()
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ls.DropHolder(ctx, loaded, kind, id)
}

func (hs *Handlers) RecoverSandboxLeases(ctx context.Context) {
	loaded, _ := hs.h.settings()
	hs.h.angelus.Leases.Recover(ctx, loaded)
}

func (hs *Handlers) KeepFinishingSandboxLeases(ctx context.Context) {
	hs.h.angelus.Leases.KeepFinishing(ctx, func() *config.Loaded {
		loaded, _ := hs.h.settings()
		return loaded
	})
}

func (h *handlers) sandboxList(ctx context.Context, params json.RawMessage) (interface{}, error) {
	out := rpc.SandboxListResponse{Classes: []rpc.SandboxClass{}, Leases: []rpc.SandboxLease{}}
	ls := h.angelus.Leases
	if ls == nil {
		return out, nil
	}
	for _, c := range ls.Classes() {
		out.Classes = append(out.Classes, rpc.SandboxClass{Name: c.class.Name, Node: c.node, Workload: c.class.Workload, Sandboxes: c.class.Sandboxes, Egress: c.class.Egress})
	}
	for _, r := range ls.Ledger.Rows() {
		out.Leases = append(out.Leases, rpc.SandboxLease{Node: r.Node, Class: r.Class, Generation: r.Generation, Sandbox: r.Sandbox,
			State: string(r.State), Leaser: r.Leaser, Holders: r.Holders, Since: r.Since.Format(time.RFC3339)})
	}
	return out, nil
}

func (h *handlers) sandboxReinstate(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.SandboxReinstateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if !h.callerIsOwner(ctx) {
		return nil, fmt.Errorf("sandbox reinstate: the owner reinstates a sandbox")
	}
	ls := h.angelus.Leases
	if ls == nil {
		return nil, fmt.Errorf("sandbox reinstate: this daemon names no sandbox node")
	}
	loaded, _ := h.settings()
	if err := ls.Reinstate(ctx, loaded, req.Sandbox); err != nil {
		return nil, err
	}
	return rpc.SandboxReinstateResponse{Sandbox: req.Sandbox}, nil
}
