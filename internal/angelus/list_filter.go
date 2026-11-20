package angelus

import (
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func listFiltered(req rpc.ListRequest) bool {
	return req.Under != "" || req.Holders || req.Match != ""
}

func (h *handlers) filterList(rows []rpc.FigaroInfoResponse, tasks []listEnrichment, req rpc.ListRequest) ([]rpc.FigaroInfoResponse, []listEnrichment) {
	keep := func(pass func(rpc.FigaroInfoResponse) bool) {
		rows = slices.DeleteFunc(slices.Clone(rows), func(r rpc.FigaroInfoResponse) bool { return !pass(r) })
	}
	if req.Under != "" {
		under := map[string]bool{}
		for _, id := range h.subtree(req.Under) {
			under[id] = true
		}
		keep(func(r rpc.FigaroInfoResponse) bool { return under[r.ID] })
	}
	if req.Holders {
		var held map[string][]string
		if namer, ok := h.angelus.Backend.(roleNamer); ok {
			held = namer.RoleNamesByTarget()
		}
		keep(func(r rpc.FigaroInfoResponse) bool { return len(held[r.ID]) > 0 })
	}
	tasks = pageEnrichments(rows, tasks)
	if req.Match != "" {
		h.enrichList(rows, tasks)
		tasks = nil
		want := strings.ToLower(req.Match)
		keep(func(r rpc.FigaroInfoResponse) bool {
			_, handle := names(h.angelus.Backend, r.ID)
			return strings.Contains(strings.ToLower(handle), want) || strings.Contains(strings.ToLower(r.Mantra), want)
		})
	}
	return rows, pageEnrichments(rows, tasks)
}

func (h *handlers) fillLastTurns(rows []rpc.FigaroInfoResponse) {
	if h.angelus.Backend == nil {
		return
	}
	for i := range rows {
		if rows[i].Node == "" && rows[i].State != "anchor" && rows[i].State != "form" {
			rows[i].LastTurn = h.newestTurn(rows[i].ID)
		}
	}
}

func (h *handlers) newestTurn(id string) uint64 {
	log, err := h.angelus.Backend.OpenFigIR(id)
	if err != nil {
		return 0
	}
	tail, ok := log.PeekTail()
	if !ok {
		return 0
	}
	if tail.Payload.TurnID != 0 {
		return tail.Payload.TurnID
	}
	turn, _ := store.TurnAtLT(log, tail.LT)
	return turn
}
