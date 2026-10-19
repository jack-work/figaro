package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

var errStaticTroupe = errors.New("this node's troupe is static: it keeps no signed entries (troupe.provider = \"figaro\" does)")

func (h *handlers) ledger() (*peer.Ledger, error) {
	m, ok := h.angelus.Peers.(*peer.Ledger)
	if !ok {
		return nil, errStaticTroupe
	}
	return m, nil
}

func (h *handlers) troupeEntries(ctx context.Context, params json.RawMessage) (any, error) {
	m, err := h.ledger()
	if err != nil {
		return nil, err
	}
	var req rpc.TroupeEntriesRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Troupe != m.Troupe() {
		return nil, fmt.Errorf("this node is in troupe %s, not %q", m.Troupe(), req.Troupe)
	}
	if _, err := asPeerNode(ctx, rpc.MethodTroupeEntries); err != nil {
		if aerr := h.allowed(ctx, rpc.VerbAdmin, authz.Troupe(m.Troupe())); aerr != nil {
			return nil, aerr
		}
	}
	es, err := troupe.Decode(req.Entries)
	if err != nil {
		return nil, err
	}
	if len(es) > 0 {
		if _, err := m.Merge(es...); err != nil {
			slog.Warn("troupe entries refused", "err", err)
		}
	}
	return rpc.TroupeEntriesResponse{Entries: troupe.Encode(m.Entries())}, nil
}
