package angelus

import (
	"cmp"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

func (h *handlers) apiKeyLedger(name string) (*peer.Ledger, error) {
	m, err := h.ledger()
	if err != nil {
		return nil, err
	}
	if name != m.Troupe() {
		return nil, fmt.Errorf("this node is in troupe %s, not %q", m.Troupe(), name)
	}
	return m, nil
}

func recordedPrincipal(ctx context.Context) (string, error) {
	by := authz.PrincipalText(authz.PrincipalOf(ctx))
	if err := troupe.ValidOwner(by); err != nil {
		return "", fmt.Errorf("no principal to record: %w", err)
	}
	return by, nil
}

func (h *handlers) troupeAPIKeyMint(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupeAPIKeyMintRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	m, err := h.apiKeyLedger(req.Troupe)
	if err != nil {
		return nil, err
	}
	if err := h.allowed(ctx, rpc.VerbAdmin, authz.Troupe(m.Troupe())); err != nil {
		return nil, err
	}
	if err := apikey.ValidID(req.ID); err != nil {
		return nil, err
	}
	if _, err := apikey.ParsePrincipal(req.Principal, m.Troupe()); err != nil {
		return nil, err
	}
	if len(req.Hash) != apikey.HashSize {
		return nil, fmt.Errorf("an api key hash is %d bytes", apikey.HashSize)
	}
	ttl := time.Duration(req.TTLMillis) * time.Millisecond
	if ttl == 0 {
		ttl = apikey.DefaultTTL
	}
	if ttl < time.Minute || ttl > apikey.MaxTTL {
		return nil, fmt.Errorf("an api key lives from a minute to %s", apikey.MaxTTL)
	}
	by, err := recordedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, taken := m.APIKeys()[req.ID]; taken {
		return nil, fmt.Errorf("api key id %s is taken; mint again", req.ID)
	}
	nodes, err := keyAudience(m, req.Nodes)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	inter, ikey, err := h.angelus.Troupe.Dir.Intermediate()
	if errors.Is(err, fs.ErrNotExist) || err == nil && !now.Before(inter.NotAfter) {
		return nil, cannotSign(m, m.View(), now)
	}
	if err != nil {
		return nil, err
	}
	e, err := troupe.SignAPIKey(ikey, inter, m.Troupe(), req.ID, req.Principal, req.Hash, now.Add(ttl), nodes, by, now)
	if err != nil {
		return nil, err
	}
	if _, err := m.Merge(e); err != nil {
		return nil, err
	}
	rec := m.APIKeys()[req.ID]
	slog.Info("troupe api key minted", "troupe", m.Troupe(), "id", rec.ID, "principal", rec.Principal, "by", rec.By, "node", rec.Signer, "nodes", strings.Join(rec.Nodes, ","), "not_after", rec.NotAfter.UTC().Format(time.RFC3339))
	out := rpc.TroupeAPIKeyMintResponse{Key: apiKeyRow(rec)}
	out.Reached, out.Unreachable = h.spread(ctx)
	return out, nil
}

func (h *handlers) troupeAPIKeyRevoke(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupeAPIKeyRevokeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	m, err := h.apiKeyLedger(req.Troupe)
	if err != nil {
		return nil, err
	}
	by, err := recordedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rec, ok := m.APIKeys()[req.ID]
	if !ok {
		return nil, fmt.Errorf("troupe %s holds no api key %q", m.Troupe(), req.ID)
	}
	if by != rec.By && by != rec.Principal {
		if err := h.allowed(ctx, rpc.VerbAdmin, authz.Troupe(m.Troupe())); err != nil {
			return nil, fmt.Errorf("revoking api key %s takes its minter, its own principal, or %w", req.ID, err)
		}
	}
	if rec.Revoked != nil {
		return rpc.TroupeAPIKeyRevokeResponse{Key: apiKeyRow(rec), Already: true}, nil
	}
	cur, err := h.angelus.Troupe.Current()
	if err != nil {
		return nil, fmt.Errorf("this node has no current certificate to sign the revoke with: %w", err)
	}
	issuer, err := x509.ParseCertificate(cur.Certificate[1])
	if err != nil {
		return nil, err
	}
	signer, ok := cur.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("this node's key cannot sign")
	}
	e, err := troupe.SignAPIKeyRevoke(signer, cur.Leaf, issuer, m.Troupe(), m.Self(), req.ID, by, time.Now())
	if err != nil {
		return nil, err
	}
	if _, err := m.Merge(e); err != nil {
		return nil, err
	}
	rec = m.APIKeys()[req.ID]
	slog.Info("troupe api key revoked", "troupe", m.Troupe(), "id", rec.ID, "principal", rec.Principal, "by", by, "node", m.Self(), "minted_by", rec.By)
	out := rpc.TroupeAPIKeyRevokeResponse{Key: apiKeyRow(rec)}
	out.Reached, out.Unreachable = h.spread(ctx)
	return out, nil
}

func (h *handlers) troupeAPIKeyList(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupeAPIKeyListRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	who := authz.PrincipalText(authz.PrincipalOf(ctx))
	m, err := h.ledger()
	if errors.Is(err, errStaticTroupe) {
		return rpc.TroupeAPIKeyListResponse{Caller: who, Keys: []rpc.TroupeAPIKey{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if req.Troupe != "" && req.Troupe != m.Troupe() {
		return nil, fmt.Errorf("this node is in troupe %s, not %q", m.Troupe(), req.Troupe)
	}
	all := h.allowed(ctx, rpc.VerbAdmin, authz.Troupe(m.Troupe())) == nil
	out := rpc.TroupeAPIKeyListResponse{Troupe: m.Troupe(), Caller: who, All: all, Keys: []rpc.TroupeAPIKey{}}
	for _, rec := range m.APIKeys() {
		if all || who != "" && (rec.By == who || rec.Principal == who) {
			out.Keys = append(out.Keys, apiKeyRow(rec))
		}
	}
	slices.SortFunc(out.Keys, func(a, b rpc.TroupeAPIKey) int {
		return cmp.Or(cmp.Compare(a.MintedAt, b.MintedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func keyAudience(m *peer.Ledger, asked []string) ([]string, error) {
	if len(asked) == 0 {
		return []string{m.Self()}, nil
	}
	if len(asked) == 1 && asked[0] == apikey.AllNodes {
		return asked, nil
	}
	known := append(slices.Sorted(maps.Keys(m.View().Members)), m.Self())
	var out []string
	for _, ref := range asked {
		node, err := memberNamed(known, ref)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, node) {
			out = append(out, node)
		}
	}
	slices.Sort(out)
	return out, troupe.CheckAudience(out)
}

func memberNamed(known []string, ref string) (string, error) {
	if ref == apikey.AllNodes {
		return "", fmt.Errorf("%q names every node on its own, not beside others", apikey.AllNodes)
	}
	var found []string
	for _, n := range known {
		if n == ref {
			return n, nil
		}
		if len(ref) >= rpc.NodeShortLen && strings.HasPrefix(n, ref) && !slices.Contains(found, n) {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no member of this troupe is node %q", ref)
	}
	return "", fmt.Errorf("node %q names %s", ref, strings.Join(found, " and "))
}

func apiKeyRow(r troupe.APIKeyRecord) rpc.TroupeAPIKey {
	row := rpc.TroupeAPIKey{ID: r.ID, Principal: r.Principal, By: r.By, Signer: r.Signer, MintedAt: r.At.UnixMilli(), NotAfter: r.NotAfter.UnixMilli(), Nodes: r.Nodes, Void: r.Void}
	if r.Revoked != nil {
		row.RevokedBy, row.RevokedVia, row.RevokedAt = r.Revoked.By, r.Revoked.Node, r.Revoked.At.UnixMilli()
	}
	return row
}
