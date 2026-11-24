package angelus

import (
	"context"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func (h *handlers) unlistedRoleAncestors(ctx context.Context, rows []rpc.FigaroInfoResponse) []rpc.FigaroInfoResponse {
	if h.angelus.Backend == nil {
		return nil
	}
	listed := make(map[string]bool, len(rows))
	for i := range rows {
		listed[rows[i].ID] = true
	}
	var nodes map[string]store.NodeView
	var out []rpc.FigaroInfoResponse
	var climb func(up string)
	climb = func(up string) {
		if up == "" || listed[up] {
			return
		}
		if nodes == nil {
			all := h.angelus.Backend.Nodes()
			nodes = make(map[string]store.NodeView, len(all))
			for _, n := range all {
				nodes[n.ID] = n
			}
		}
		n, ok := nodes[up]
		if !ok || n.Kind != string(store.KindForm) {
			return
		}
		listed[up] = true
		l, ok := h.formListing(n.ID)
		if !ok || (l.Name == "" && l.Target == "") {
			return
		}
		row := rpc.FigaroInfoResponse{ID: n.ID, State: "form", Kind: n.Kind, Parent: n.Parent, Present: n.Present, Name: l.Name, TargetAria: l.Target}
		out = append(out, row)
		climb(drawnUnder(&row))
	}
	for i := range rows {
		climb(drawnUnder(&rows[i]))
	}
	if len(out) == 0 {
		return nil
	}
	readable, _, _ := h.readable(ctx, out)
	return readable
}
