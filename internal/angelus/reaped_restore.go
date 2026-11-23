package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

func (h *handlers) reapedRestore(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.ReapedRestoreRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return nil, err
	}
	var group *reapedGroup
	for _, g := range h.angelus.reapedGroups() {
		if slices.Contains(g.nodes, req.FigaroID) {
			group = &g
			break
		}
	}
	if group == nil {
		return nil, fmt.Errorf("reaped restore: %s is not reaped", req.FigaroID)
	}
	if err := h.mayRestore(ctx, *group); err != nil {
		return nil, fmt.Errorf("reaped restore %s: %w", group.root, err)
	}
	return h.restoreGroup(ctx, *group, req.Keep)
}

func (h *handlers) mayRestore(ctx context.Context, group reapedGroup) error {
	if box, ok := callerBox(ctx); ok {
		return fmt.Errorf("box %s may not restore a reaped aria: its purge is what the box's grace promises", box)
	}
	for _, id := range group.nodes {
		if err := h.allowed(ctx, rpc.VerbSet, authz.Aria(id)); err != nil {
			return err
		}
	}
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return err
	}
	if _, err = h.angelus.topology().Check(ctx, by, hierarchy.Plan{Op: hierarchy.Restore, Revive: group.nodes}); err != nil {
		return err
	}
	return h.refuseRestoreOverMaxChildren(group)
}

func (h *handlers) refuseRestoreOverMaxChildren(group reapedGroup) error {
	returning := map[string]int{}
	for _, id := range group.nodes {
		if meta, _ := h.angelus.Backend.Meta(id); meta != nil && meta.RetiredAtMS > 0 {
			continue
		}
		returning[h.angelus.drawnParentOf(id)]++
	}
	return h.refuseReturningOverMaxChildren("restoring", returning)
}

func (h *handlers) refuseReturningOverMaxChildren(verb string, returning map[string]int) error {
	for parent, n := range returning {
		max, capped, err := h.angelus.fanoutLimit(parent)
		if err != nil {
			return fmt.Errorf("the limit of %s cannot be read to admit %s: %w", parent, verb, err)
		}
		if held := h.angelus.liveChildCount(parent); capped && held+n > max {
			return fmt.Errorf("%s %d would put %s over max_children %d with %d live children: %w", verb, n, parent, max, held, maxChildrenRefusal(parent, held, max))
		}
	}
	return nil
}

func (h *handlers) refuseRevivalOverMaxChildren(id string, patch form.Patch) error {
	meta, _ := h.angelus.Backend.Meta(id)
	if meta == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	if meta.CountsAt(now) {
		return nil
	}
	if after := meta.AfterPatch(id, patch, now); !after.CountsAt(now) {
		return nil
	}
	return h.refuseReturningOverMaxChildren("reviving", map[string]int{h.angelus.drawnParentOf(id): 1})
}

func (h *handlers) restoreGroup(ctx context.Context, group reapedGroup, keep bool) (rpc.ReapedRestoreResponse, error) {
	resp := rpc.ReapedRestoreResponse{Root: group.root}
	rk, ok := h.angelus.Backend.(reapedKeeper)
	if !ok {
		return resp, fmt.Errorf("reaped restore: this store keeps no reaped index")
	}
	lifetime := form.Build(form.Snapshot{}, nil, []string{store.SystemTTLKey, store.SystemReapIfEmptyKey})
	if keep {
		snap, err := h.angelus.Backend.FormState(group.root)
		if err != nil {
			return resp, err
		}
		if err := h.refuseCallerKeys(ctx, lineage{of: group.root}, snap, &lifetime); err != nil {
			return resp, fmt.Errorf("reaped restore %s --keep: %w", group.root, err)
		}
	}
	unreap := form.Build(form.Snapshot{}, nil, reapKeys)
	defer func() { h.angelus.settleFanout(hierarchy.Plan{Op: hierarchy.Restore, Revive: resp.Restored}) }()
	for _, id := range group.nodes {
		patch := unreap
		if keep && id == group.root {
			patch = form.Merge(unreap, lifetime)
		}
		if _, err := rk.ApplyFormPrivileged(id, patch); err != nil {
			return resp, fmt.Errorf("reaped restore %s: stopped at %s after restoring %d of %d (%s): %w",
				group.root, id, len(resp.Restored), len(group.nodes), strings.Join(resp.Restored, ", "), err)
		}
		if t, ok := h.angelus.Backend.(toucher); ok {
			t.Touch(id, time.Now())
		}
		resp.Restored = append(resp.Restored, id)
	}
	h.refreshReapedForm()
	return resp, nil
}
