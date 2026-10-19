package angelus

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jack-work/figaro/api/rpc"
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
	for _, node := range m.Nodes() {
		row := rpc.TroupeMember{Node: node}
		if mem, ok := m.Member(node); ok {
			row.Endpoint = "tls://" + mem.Address
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
