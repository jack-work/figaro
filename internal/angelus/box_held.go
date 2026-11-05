package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

const boxFormsMax = 256

func boxHold(box string) form.Patch {
	raw, _ := json.Marshal(box)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": raw}, nil)
}

func (h *handlers) holdInCallerBox(ctx context.Context, patch form.Patch) (form.Patch, error) {
	box, ok := callerBox(ctx)
	if !ok {
		return patch, nil
	}
	if n := h.formsMintedBy(authz.PrincipalText(authz.PrincipalOf(ctx))); n >= boxFormsMax {
		return form.Patch{}, fmt.Errorf("box %s holds %d unbound forms, the most a box may mint", box, n)
	}
	return form.Merge(patch, boxHold(box)), nil
}

func (h *handlers) formsMintedBy(principal string) int {
	n := 0
	for _, node := range h.angelus.Backend.Nodes() {
		if node.Kind != string(store.KindForm) {
			continue
		}
		if m, _ := h.angelus.Backend.Meta(node.ID); m != nil && m.MintedBy == principal {
			n++
		}
	}
	return n
}

func (h *handlers) heldBox(snap form.Snapshot) string {
	v := snap.Lookup("system.sandbox")
	if v == nil || *v == "" {
		return ""
	}
	loaded, _ := h.settings()
	if _, ok := loaded.Box(*v); !ok {
		return ""
	}
	return *v
}

func (h *handlers) refuseRoleOutOfBox(snap form.Snapshot) error {
	box := h.heldBox(snap)
	if box == "" {
		return nil
	}
	target := snap.Lookup("target-aria")
	if target == nil || *target == "" {
		return nil
	}
	if got := h.sandboxOf(*target); got != box {
		return fmt.Errorf("a role held in box %s points only at an aria in it; %s runs in sandbox %q", box, *target, sandboxName(got))
	}
	return nil
}

func (h *handlers) holdCastInBox(ctx context.Context, method, id string, params json.RawMessage) (json.RawMessage, error) {
	if method != rpc.MethodCast {
		return params, nil
	}
	var req rpc.CastRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.refuseUnheldSandbox(ctx, req.RolePatch); err != nil {
		return nil, fmt.Errorf("cast: %w", err)
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	seat := form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil)
	if req.RolePatch == nil || req.RolePatch.IsIdentity() {
		if req.FormID == "" {
			return params, nil
		}
		snap, err := h.angelus.Backend.FormState(req.FormID)
		if err != nil {
			return nil, err
		}
		if err := h.refuseRoleOutOfBox(snap.Apply(seat)); err != nil {
			return nil, fmt.Errorf("cast: %w", err)
		}
		return params, nil
	}
	role, err := h.holdInCallerBox(ctx, *req.RolePatch)
	if err != nil {
		return nil, fmt.Errorf("cast: %w", err)
	}
	if err := h.refuseRoleOutOfBox(form.Snapshot{}.Apply(form.Merge(role, seat))); err != nil {
		return nil, fmt.Errorf("cast: %w", err)
	}
	req.RolePatch = &role
	return json.Marshal(req)
}
