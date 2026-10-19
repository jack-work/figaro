package angelus

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

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
	_, notPeer := asPeerNode(ctx, rpc.MethodTroupeEntries)
	if notPeer != nil {
		if err := h.allowed(ctx, rpc.VerbAdmin, authz.Troupe(m.Troupe())); err != nil {
			return nil, err
		}
	}
	es, err := troupe.Decode(req.Entries)
	if err != nil {
		return nil, err
	}
	var added []troupe.Entry
	if len(es) > 0 {
		if added, err = m.Merge(es...); err != nil {
			slog.Warn("troupe entries refused", "err", err)
			if notPeer != nil && len(added) == 0 {
				return nil, err
			}
		}
	}
	resp := rpc.TroupeEntriesResponse{}
	if notPeer != nil && len(added) > 0 {
		resp.Reached, resp.Unreachable = h.spread(ctx)
	}
	resp.Entries = troupe.Encode(m.Entries())
	resp.Version, resp.Policy = m.Version(), m.PolicyVersion()
	return resp, nil
}

func (h *handlers) spread(ctx context.Context) ([]string, []rpc.NodeUnreachable) {
	if h.angelus.TroupeSpread == nil {
		return nil, nil
	}
	reached := []string{}
	var missed []rpc.NodeUnreachable
	for node, err := range h.angelus.TroupeSpread(ctx) {
		if err != nil {
			missed = append(missed, rpc.NodeUnreachable{Node: node, Error: err.Error()})
			continue
		}
		reached = append(reached, node)
	}
	slices.Sort(reached)
	slices.SortFunc(missed, func(a, b rpc.NodeUnreachable) int { return strings.Compare(a.Node, b.Node) })
	return reached, missed
}

func (h *handlers) troupeLeave(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupeLeaveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	m, err := h.troupeLedger(req.Troupe)
	if err != nil {
		return nil, err
	}
	cur, err := h.angelus.Troupe.Current()
	if err != nil {
		return nil, fmt.Errorf("this node has no current certificate to sign its leave with: %w", err)
	}
	issuer, err := x509.ParseCertificate(cur.Certificate[1])
	if err != nil {
		return nil, err
	}
	signer, ok := cur.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("this node's key cannot sign")
	}
	e, err := troupe.SignLeave(signer, cur.Leaf, issuer, m.Troupe(), m.Self(), time.Now())
	if err != nil {
		return nil, err
	}
	if _, err := m.Merge(e); err != nil {
		return nil, err
	}
	slog.Info("troupe: this node left", "troupe", m.Troupe(), "node", m.Self())
	out := rpc.TroupeLeaveResponse{Node: m.Self()}
	out.Reached, out.Unreachable = h.spread(ctx)
	return out, nil
}
