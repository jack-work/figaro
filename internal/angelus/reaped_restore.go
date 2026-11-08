package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
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
	for _, id := range group.nodes {
		if err := h.allowed(ctx, rpc.VerbSet, authz.Aria(id)); err != nil {
			return err
		}
	}
	return nil
}

func (h *handlers) restoreGroup(ctx context.Context, group reapedGroup, keep bool) (rpc.ReapedRestoreResponse, error) {
	resp := rpc.ReapedRestoreResponse{Root: group.root}
	for _, id := range group.nodes {
		drops := []string{store.SystemReapedKey, store.SystemReapedUntilKey, store.SystemReapedByKey}
		if keep && id == group.root {
			drops = append(drops, store.SystemTTLKey, store.SystemReapIfEmptyKey)
		}
		raw, err := json.Marshal(rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, drops)})
		if err != nil {
			return resp, err
		}
		withAria, err := rpc.WithAria(raw, id)
		if err != nil {
			return resp, err
		}
		if _, err := h.ariaMethod(rpc.MethodSet)(ctx, withAria); err != nil {
			return resp, fmt.Errorf("reaped restore %s: stopped at %s after restoring %d of %d (%s): %w",
				group.root, id, len(resp.Restored), len(group.nodes), strings.Join(resp.Restored, ", "), err)
		}
		resp.Restored = append(resp.Restored, id)
	}
	h.refreshReapedForm()
	return resp, nil
}
