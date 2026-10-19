package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

var localOnlyRights = []string{"use on sandbox:local", "admin on the troupe (its policy, revoke, leave)"}

func (h *handlers) troupePolicySet(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupePolicySetRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	m, err := h.troupeLedger(req.Troupe)
	if err != nil {
		return nil, err
	}
	if err := h.allowed(ctx, rpc.VerbAdmin, authz.Troupe(m.Troupe())); err != nil {
		return nil, err
	}
	if len(req.Body) > troupe.MaxPolicyBody {
		return nil, fmt.Errorf("a troupe policy holds at most %d bytes", troupe.MaxPolicyBody)
	}
	if err := authz.CheckTroupeDocument([]byte(req.Body), m.Troupe(), nil); err != nil {
		return nil, err
	}
	by := authz.PrincipalText(authz.PrincipalOf(ctx))
	if err := troupe.ValidOwner(by); err != nil {
		return nil, fmt.Errorf("no principal to record as the policy's author: %w", err)
	}
	h.policySign.Lock()
	defer h.policySign.Unlock()
	now := time.Now()
	view := m.View()
	inter, ikey, err := h.angelus.Troupe.Dir.Intermediate()
	if errors.Is(err, fs.ErrNotExist) || err == nil && !now.Before(inter.NotAfter) {
		return nil, cannotSign(m, view, now)
	}
	if err != nil {
		return nil, err
	}
	if view.Policy == nil {
		return nil, fmt.Errorf("troupe %s has no policy yet, and the root signs the first: op read <root ref> | figaro troupe policy set %s <file> --root", m.Troupe(), m.Troupe())
	}
	if setters, _ := troupe.PolicySetters(view.Policy.Body); !slices.Contains(setters, m.Self()) {
		return nil, fmt.Errorf("version %d names %v as setters, not this node (%s): sign from one of them, or with the root (--root)", view.Policy.Version, setters, m.Self())
	}
	version := view.Policy.Version + 1
	e, err := troupe.SignPolicy(ikey, inter, m.Troupe(), version, by, req.Body, now)
	if err != nil {
		return nil, err
	}
	if _, err := m.Merge(e); err != nil {
		return nil, err
	}
	slog.Info("troupe policy signed", "troupe", m.Troupe(), "version", version, "by", by)
	out := rpc.TroupePolicySetResponse{Version: version, Signer: m.Self()}
	out.Reached, out.Unreachable = h.spread(ctx)
	if out.Reached == nil {
		out.Reached = []string{}
	}
	return out, nil
}

func (h *handlers) troupePolicy(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.TroupePolicyRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	out := rpc.TroupePolicyResponse{LocalOnly: localOnlyRights}
	if h.angelus.Policies != nil {
		out.Troupe = h.angelus.Policies.TroupeName()
		if err := h.angelus.Policies.TroupeLoadError(); err != nil {
			out.LoadError = err.Error()
		}
	}
	m, ok := h.angelus.Peers.(*peer.Ledger)
	if !ok {
		out.Provider = config.ProviderStatic
		loaded, _ := h.settings()
		if loaded != nil {
			out.Path, _ = loaded.StaticTroupePolicy()
		}
		return out, nil
	}
	if req.Troupe != "" && req.Troupe != m.Troupe() {
		return nil, fmt.Errorf("this node is in troupe %s, not %q", m.Troupe(), req.Troupe)
	}
	out.Troupe, out.Provider = m.Troupe(), config.ProviderFigaro
	view := m.View()
	if view.Policy != nil {
		v := policyVersion(*view.Policy)
		out.InForce, out.Body = &v, view.Policy.Body
	}
	for _, e := range view.Rivals {
		out.Rivals = append(out.Rivals, policyVersion(e))
	}
	for _, e := range view.Voided {
		out.Voided = append(out.Voided, policyVersion(e))
	}
	for _, e := range view.Refused {
		out.Refused = append(out.Refused, policyVersion(e))
	}
	seen := h.angelus.PeerVersions.All()
	for _, node := range slices.Sorted(maps.Keys(view.Members)) {
		row := rpc.TroupePolicySighting{Node: node}
		switch s, ok := seen[node]; {
		case node == m.Self():
			row.Self, row.Policy = true, m.PolicyVersion()
		case ok:
			row.Policy, row.SeenAt = s.Policy, s.At.UnixMilli()
		}
		out.Nodes = append(out.Nodes, row)
	}
	return out, nil
}

func policyVersion(e troupe.Entry) rpc.TroupePolicyVersion {
	return rpc.TroupePolicyVersion{Version: e.Version, Root: e.RootSigned(), Signer: e.Node, By: e.By, At: e.At}
}
