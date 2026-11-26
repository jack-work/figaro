package angelus

import (
	"context"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func (h *handlers) unlistedForms(ctx context.Context, rows []rpc.FigaroInfoResponse) []rpc.FigaroInfoResponse {
	if h.angelus.Backend == nil {
		return nil
	}
	listed := make(map[string]bool, len(rows))
	for i := range rows {
		listed[rows[i].ID] = true
	}
	all := h.angelus.Backend.Nodes()
	nodes := make(map[string]store.NodeView, len(all))
	for _, n := range all {
		nodes[n.ID] = n
	}
	var out []rpc.FigaroInfoResponse
	var climb func(up string)
	climb = func(up string) {
		if up == "" || listed[up] {
			return
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
		row := rpc.FigaroInfoResponse{ID: n.ID, State: "form", Kind: n.Kind, Parent: n.Parent, Present: n.Present, Name: l.Name, TargetAria: l.Target, LastActive: h.angelus.Backend.LastTS(n.ID)}
		out = append(out, row)
		climb(drawnUnder(&row))
	}
	for i := range rows {
		climb(drawnUnder(&rows[i]))
	}
	for _, n := range all {
		if n.Kind == string(store.KindForm) {
			climb(n.ID)
		}
	}
	if len(out) == 0 {
		return nil
	}
	readable, _, _ := h.readable(ctx, out)
	return readable
}
