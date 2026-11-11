package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

func topologySnapshot(rows []rpc.FigaroInfoResponse) form.Snapshot {
	listed := make(map[string]bool, len(rows))
	for _, r := range rows {
		listed[r.ID] = true
	}
	shown := func(id string) string {
		if listed[id] {
			return id
		}
		return ""
	}
	keys := map[string]json.RawMessage{}
	for _, r := range rows {
		if up := shown(drawnUnder(&r)); up != shown(r.Parent) {
			keys[r.ID], _ = json.Marshal(up)
		}
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
}

func (h *handlers) arrange(ctx context.Context, shown *store.Form, params json.RawMessage) (rpc.SetResponse, error) {
	var req rpc.SetRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return rpc.SetResponse{}, err
	}
	cur, version := shown.Snapshot()
	if req.IfVersion != 0 && req.IfVersion != version {
		return rpc.SetResponse{}, fmt.Errorf("%s moved on: at version %d, not %d", rpc.TopologyFormID, version, req.IfVersion)
	}
	parents := map[string]string{}
	var restore, set, removed []string
	var moves [][2]string
	for _, ent := range req.Patch.Entries() {
		id, err := h.canonicalID(ctx, ent.Key)
		if err != nil {
			return rpc.SetResponse{}, err
		}
		if err := h.allowed(ctx, rpc.VerbSet, authz.Aria(id)); err != nil {
			return rpc.SetResponse{}, err
		}
		if ent.IsRemoval() {
			if _, ok := cur.Get(id); !ok && req.Assert {
				return rpc.SetResponse{}, fmt.Errorf("%s: %s is drawn where its history puts it", rpc.TopologyFormID, ent.Key)
			}
			restore = append(restore, id)
			removed = append(removed, ent.Key)
			continue
		}
		var ref string
		if err := json.Unmarshal(ent.New, &ref); err != nil {
			return rpc.SetResponse{}, fmt.Errorf("%s: %s takes the aria or form to move it under, or \"\" to draw it at the top, not %s", rpc.TopologyFormID, ent.Key, ent.New)
		}
		parent := ""
		if ref != "" {
			if parent, err = h.canonicalID(ctx, ref); err != nil {
				return rpc.SetResponse{}, err
			}
			if h.allowed(ctx, rpc.VerbSet, authz.Aria(parent)) != nil {
				return rpc.SetResponse{}, fmt.Errorf("aria %s not found", ref)
			}
		}
		set = append(set, ent.Key)
		if parent == "" {
			parents[id] = parent
			continue
		}
		moves = append(moves, [2]string{id, parent})
		if _, drawn := cur.Get(id); drawn {
			restore = append(restore, id)
		}
	}
	if len(parents) == 0 && len(restore) == 0 && len(moves) == 0 {
		return rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: version}, nil
	}
	draw := func(m [2]string) {
		parents[m[0]] = m[1]
		restore = slices.DeleteFunc(restore, func(id string) bool { return id == m[0] })
	}
	owner := h.callerIsOwner(ctx)
	for _, m := range moves {
		if !owner {
			draw(m)
			continue
		}
		err := h.angelus.Backend.Move(m[0], m[1])
		switch {
		case err == nil:
		case errors.Is(err, store.ErrMoveCopies), errors.Is(err, store.ErrMoveCycle):
			draw(m)
		default:
			return rpc.SetResponse{}, err
		}
	}
	if len(parents) == 0 && len(restore) == 0 {
		return rpc.SetResponse{OK: true, Set: set, Outcome: rpc.OutcomeApplied}, nil
	}
	err := h.angelus.Backend.Arrange(parents, restore, func(id string) error {
		return h.allowed(ctx, rpc.VerbSet, authz.Aria(id))
	})
	if errors.Is(err, store.ErrNoTrunkCapability) {
		return rpc.SetResponse{}, fmt.Errorf("%s: this daemon keeps no presentation hierarchy", rpc.TopologyFormID)
	}
	if err != nil {
		return rpc.SetResponse{}, err
	}
	return rpc.SetResponse{OK: true, Set: set, Remove: removed, Outcome: rpc.OutcomeApplied}, nil
}
