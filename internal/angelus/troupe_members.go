package angelus

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

func (h *handlers) troupeMembers(context.Context, json.RawMessage) (any, error) {
	out := rpc.TroupeMembersResponse{Peers: []rpc.TroupeMember{}}
	if h.angelus.Backend != nil {
		out.Node = h.angelus.Backend.NodeID()
	}
	if k := h.angelus.Troupe; k != nil {
		st := k.Status()
		out.Keys = &rpc.TroupeKeys{Troupe: st.Troupe, NodeExpires: unixMilli(st.NodeExpires), IntermediateExpires: unixMilli(st.IntermediateExpires)}
		if st.Err != nil {
			out.Keys.Error = st.Err.Error()
		}
	}
	m := h.angelus.Peers
	if m == nil {
		return out, nil
	}
	out.Version = m.Version()
	var view troupe.View
	if l, ok := m.(*peer.Ledger); ok {
		view = l.View()
		out.Provider = config.ProviderFigaro
		out.AdmittedBy = view.Members[l.Self()].AdmittedBy
		for _, node := range slices.Sorted(maps.Keys(view.Gone)) {
			out.Gone = append(out.Gone, rpc.TroupeGone{Node: node, By: string(view.Gone[node])})
		}
	}
	now := time.Now()
	for _, node := range m.Nodes() {
		row := rpc.TroupeMember{Node: node}
		if mem, ok := m.Member(node); ok {
			row.Endpoint = "tls://" + mem.Address
		}
		if mem, ok := view.Members[node]; ok {
			row.AdmittedBy, row.Signs = mem.AdmittedBy, now.Before(mem.SignsUntil)
		}
		if s, ok := h.angelus.PeerVersions.Seen(node); ok {
			row.Met, row.Version, row.SeenAt, row.Drift = true, s.Version, s.At.UnixMilli(), s.Version != out.Version
		}
		out.Peers = append(out.Peers, row)
	}
	return out, nil
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
