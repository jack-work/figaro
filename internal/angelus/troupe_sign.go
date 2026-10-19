package angelus

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

func (h *handlers) troupeLedger(name string) (*peer.Ledger, error) {
	m, err := h.ledger()
	if err != nil {
		return nil, err
	}
	if name != m.Troupe() {
		return nil, fmt.Errorf("this node is in troupe %s, not %q", m.Troupe(), name)
	}
	if h.angelus.Troupe == nil {
		return nil, errors.New("this node holds no troupe keys")
	}
	return m, nil
}

func (h *handlers) troupeIdentity(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupeIdentityRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	m, err := h.troupeLedger(req.Troupe)
	if err != nil {
		return nil, err
	}
	if err := h.allowed(ctx, rpc.VerbJoin, authz.Troupe(m.Troupe())); err != nil {
		return nil, err
	}
	root, err := h.angelus.Troupe.Dir.Root()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(root.Raw)
	return rpc.TroupeIdentityResponse{
		Troupe: m.Troupe(), Node: m.Self(), Principal: authz.PrincipalOf(ctx),
		Root: root.Raw, RootSHA256: hex.EncodeToString(sum[:]), Signers: m.View().Signers(time.Now()),
	}, nil
}

func (h *handlers) troupeSign(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupeSignRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	m, err := h.troupeLedger(req.Troupe)
	if err != nil {
		return nil, err
	}
	pub, err := checkSignRequest(req)
	if err != nil {
		return nil, err
	}
	via, notPeer := asPeerNode(ctx, rpc.MethodTroupeSign)
	renewal := notPeer == nil
	switch {
	case renewal && via != req.Node:
		return nil, fmt.Errorf("node %s may ask only for its own certificate, not %s's", via, req.Node)
	case renewal && !m.View().MayIssue(req.Node, m.Self()):
		return nil, fmt.Errorf("node %s is not %s's renewer, so no member would take a certificate it issued; ask one of %v", m.Self(), req.Node, m.View().Renewers[req.Node])
	case !renewal:
		if err := h.allowed(ctx, rpc.VerbJoin, authz.Troupe(m.Troupe())); err != nil {
			return nil, err
		}
	}
	view := m.View()
	if req.Node == m.Self() {
		return nil, errors.New("a node does not sign itself in through its own door")
	}
	if gone := view.Gone[req.Node]; gone != "" {
		return nil, fmt.Errorf("node %s is out of troupe %s (%s); a node id that left does not return", req.Node, m.Troupe(), gone)
	}
	if _, member := view.Members[req.Node]; member && !renewal {
		if err := h.checkRejoin(req, pub, view, m.Self()); err != nil {
			return nil, fmt.Errorf("node %s is already a member, so only its own key signs it in again: %w", req.Node, err)
		}
	}
	now := time.Now()
	inter, ikey, err := h.angelus.Troupe.Dir.Intermediate()
	if errors.Is(err, fs.ErrNotExist) || err == nil && !now.Before(inter.NotAfter) {
		return nil, cannotSign(m, view, now)
	}
	if err != nil {
		return nil, err
	}
	leaf, err := troupe.IssueNode(ikey, inter, m.Troupe(), req.Node, pub, now, troupe.NodeTTL)
	if err != nil {
		return nil, err
	}
	if mem, ok := view.Members[req.Node]; !renewal || !ok || mem.Door != req.Door {
		owner := mem.Owner
		if !renewal {
			owner = authz.PrincipalText(authz.PrincipalOf(ctx))
		}
		if err := troupe.ValidOwner(owner); err != nil {
			return nil, fmt.Errorf("no principal to record as the node's owner: %w", err)
		}
		e, err := troupe.SignJoin(ikey, inter, m.Troupe(), req.Node, req.Door, owner, now)
		if err != nil {
			return nil, err
		}
		if _, err := m.Merge(e); err != nil {
			return nil, err
		}
	}
	root, err := h.angelus.Troupe.Dir.Root()
	if err != nil {
		return nil, err
	}
	if renewal {
		slog.Info("troupe: renewed a node certificate", "node", req.Node, "until", leaf.NotAfter.Format(time.RFC3339))
	} else {
		slog.Info("troupe: signed a node in", "node", req.Node, "door", req.Door, "by", authz.PrincipalText(authz.PrincipalOf(ctx)))
	}
	return rpc.TroupeSignResponse{Chain: [][]byte{leaf.Raw, inter.Raw}, Root: root.Raw, Entries: troupe.Encode(m.Entries())}, nil
}

func checkSignRequest(req rpc.TroupeSignRequest) (ed25519.PublicKey, error) {
	if err := rpc.ValidateAriaID(req.Node); err != nil {
		return nil, fmt.Errorf("node: %w", err)
	}
	if len(req.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public_key: want %d bytes of Ed25519, got %d", ed25519.PublicKeySize, len(req.PublicKey))
	}
	host, _, err := net.SplitHostPort(req.Door)
	if err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		return nil, fmt.Errorf("door %q is not a host:port another node can dial", req.Door)
	}
	return ed25519.PublicKey(req.PublicKey), nil
}

func cannotSign(m *peer.Ledger, view troupe.View, now time.Time) error {
	signers := slices.DeleteFunc(view.Signers(now), func(n string) bool { return n == m.Self() })
	if len(signers) == 0 {
		return fmt.Errorf("node %s holds no intermediate, so it cannot sign, and no member of troupe %s that can is known here", m.Self(), m.Troupe())
	}
	named := make([]string, len(signers))
	for i, n := range signers {
		named[i] = n + " (" + view.Members[n].Door + ")"
	}
	return fmt.Errorf("node %s holds no intermediate, so it cannot sign; ask one that can: %s", m.Self(), strings.Join(named, ", "))
}

func (h *handlers) checkRejoin(req rpc.TroupeSignRequest, pub ed25519.PublicKey, view troupe.View, self string) error {
	if req.Rejoin == nil {
		return errors.New("no proof of its old key")
	}
	root, err := h.angelus.Troupe.Dir.Root()
	if err != nil {
		return err
	}
	leaf, issuer, _, err := troupe.ParseSigned(req.Rejoin.Chain, root.Raw)
	if err != nil {
		return err
	}
	by := troupe.IntermediateNode(issuer)
	if view.Gone[by] == troupe.Revoke {
		return fmt.Errorf("its old certificate came from %s, whose keys the root revoked", by)
	}
	if !view.MayIssue(req.Node, by) && by != self {
		return fmt.Errorf("its old certificate came from %s, which is neither its own intermediate, its renewer, nor this node", by)
	}
	return troupe.CheckRejoin(leaf, issuer, root, req.Rejoin.Sig, req.Troupe, req.Node, pub, req.Door)
}
