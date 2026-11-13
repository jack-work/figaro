package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/jkauthz"
)

func (h *handlers) grantSandbox(ctx context.Context, snap form.Snapshot) (form.Patch, error) {
	policies := h.angelus.Policies
	if policies == nil {
		return form.Patch{}, nil
	}
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return form.Patch{}, err
	}
	figaro := authz.FigaroOf(ctx)
	kind := ""
	if s := snap.Lookup("system.sandbox"); s != nil {
		kind = *s
	}
	if d := policies.AllowFigaro(sub, figaro, rpc.VerbUse, authz.Sandbox(kind)); !d.Allowed {
		home, ok := homeSandbox(sub.Principal)
		if !ok || sandboxName(home) == "local" {
			return form.Patch{}, fmt.Errorf("sandbox %q: %s", sandboxName(kind), d.Reason())
		}
		pin, _ := json.Marshal(home)
		return form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": pin}, nil), nil
	}
	return form.Patch{}, nil
}

func homeSandbox(p jkauthz.Principal) (string, bool) {
	if name, ok := authz.BoxOf(p); ok {
		return name, true
	}
	if p.Kind != jkauthz.KindSandbox || p.Node != "" || strings.Contains(p.ID, "@") {
		return "", false
	}
	return p.ID, true
}

func sandboxName(kind string) string {
	if kind == "" {
		return "local"
	}
	return kind
}

func (h *handlers) grantSandboxOn(ctx context.Context, id string) error {
	if h.angelus.Policies == nil {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	pin, err := h.grantSandbox(ctx, snap)
	if err != nil {
		return err
	}
	if pin.IsIdentity() {
		return nil
	}
	_, err = h.angelus.Backend.ApplyFormPrivileged(id, pin)
	return err
}

func (h *handlers) discardBirth(id string) {
	if id == "" {
		return
	}
	if err := h.RemoveAria(id, false); err != nil {
		slog.Error("a refused birth left its aria behind", "aria", id, "err", err)
	}
}

func patchTouches(p form.Patch, key string) bool {
	for _, e := range p.Entries() {
		if e.Key == key {
			return true
		}
	}
	return false
}

func (h *handlers) reloadPolicy() error {
	p := h.angelus.Policies
	if p == nil {
		return nil
	}
	loaded, _ := h.settings()
	p.SetBoxes(loaded.BoxesWithEndpoints())
	p.SetBoxesRequiringClaim(loaded.BoxesRequiringClaim())
	if err := p.Reload(loaded.PolicyPath()); err != nil {
		slog.Error("authz: policy reload failed; the previous policy stays in force", "err", err)
		return err
	}
	slog.Info("authz: policy reloaded", "path", loaded.PolicyPath())
	return h.reloadStaticTroupePolicy(loaded)
}

func (h *handlers) reloadStaticTroupePolicy(loaded *config.Loaded) error {
	if _, ledger := h.angelus.Peers.(*peer.Ledger); ledger {
		return nil
	}
	path, err := loaded.StaticTroupePolicy()
	if err != nil || path == "" {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("troupe.policy: %w", err)
	}
	if err := h.angelus.ApplyTroupeDocument(h.angelus.Policies.TroupeName(), body, loaded.Config.Authz.TakesTroupeIssuers()); err != nil {
		slog.Error("troupe policy: it grants nothing until it loads", "path", path, "err", err)
		return err
	}
	slog.Info("troupe policy reloaded", "path", path)
	return nil
}

func (h *handlers) policy() authz.Policy {
	if h.angelus.Policies == nil {
		return authz.LocalOrOwner(h.angelus.owner())
	}
	return authz.Enforce(h.angelus.Policies)
}

func (a *Angelus) owner() jkauthz.Principal {
	if a.Peercred == nil {
		return jkauthz.Principal{}
	}
	return a.Peercred.Self()
}

func (h *handlers) passesUnpoliced(ctx context.Context, sub jkauthz.Subject) bool {
	return authz.PassesUnpoliced(authz.EndpointOf(ctx), h.angelus.owner(), sub)
}

func (h *handlers) allowed(ctx context.Context, verb rpc.Verb, res authz.Resource) error {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return err
	}
	p := h.angelus.Policies
	if p == nil {
		if h.passesUnpoliced(ctx, sub) {
			return nil
		}
		return fmt.Errorf("%s on %s: no policy is loaded, so the %s door passes only the owner", verb, res, authz.EndpointOf(ctx))
	}
	if d := p.AllowFigaro(sub, authz.FigaroOf(ctx), verb, res); !d.Allowed {
		return fmt.Errorf("%s on %s: %s", verb, res, d.Reason())
	}
	return nil
}

func (h *handlers) checkAriaMethod(ctx context.Context, method, id string, params json.RawMessage) error {
	switch method {
	case rpc.MethodStudy:
		var req rpc.StudyRequest
		if json.Unmarshal(params, &req) == nil && req.FormID != "" {
			if h.allowed(ctx, rpc.VerbRead, authz.Aria(req.FormID)) != nil {
				return authz.NotFound(method, req.FormID)
			}
			if h.studiesAFigaro(req.FormID) && !h.callerIsOwner(ctx) {
				return authz.NotFound(method, req.FormID)
			}
		}
	case rpc.MethodCast:
		var req rpc.CastRequest
		if json.Unmarshal(params, &req) == nil && req.FormID != "" {
			return h.allowed(ctx, rpc.VerbSet, authz.Aria(req.FormID))
		}
	case rpc.MethodSet:
		var req rpc.SetRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return err
		}
		return h.checkFormWrite(ctx, id, req.Patch)
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return err
		}
		if req.Form != nil && req.Form.Patch != nil {
			return h.checkFormWrite(ctx, id, *req.Form.Patch)
		}
	}
	return nil
}

func (h *handlers) checkFormWrite(ctx context.Context, id string, patch form.Patch) error {
	if h.angelus.Policies == nil {
		return nil
	}
	cwd := patchTouches(patch, "system.cwd")
	held := patchTouches(patch, "system.sandbox")
	if !cwd && !held && !patchTouches(patch, "target-aria") {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	if err := h.refuseRoleOutOfBox(snap.Apply(patch)); err != nil {
		return err
	}
	if !cwd && !held {
		return nil
	}
	pin, err := h.grantSandbox(ctx, snap.Apply(patch))
	if err != nil {
		return err
	}
	if !pin.IsIdentity() {
		return fmt.Errorf("a sandbox may run an aria only in a sandbox it holds use on")
	}
	return nil
}

func (h *handlers) refuseUnheldSandbox(ctx context.Context, patch *form.Patch) error {
	if patch == nil || !patchTouches(*patch, "system.sandbox") {
		return nil
	}
	pin, err := h.grantSandbox(ctx, form.Snapshot{}.Apply(*patch))
	if err != nil {
		return err
	}
	if !pin.IsIdentity() {
		return fmt.Errorf("a sandbox may run an aria only in a sandbox it holds use on")
	}
	return nil
}

type unreadableRows struct {
	count int
	why   string
}

func (h *handlers) readable(ctx context.Context, rows []rpc.FigaroInfoResponse) ([]rpc.FigaroInfoResponse, unreadableRows) {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return nil, unreadableRows{}
	}
	if h.angelus.Policies == nil {
		if h.passesUnpoliced(ctx, sub) {
			return rows, unreadableRows{}
		}
		return nil, unreadableRows{}
	}
	view := h.angelus.Policies.Figaro(authz.FigaroOf(ctx))
	out := rows[:0]
	var refused unreadableRows
	for _, r := range rows {
		d := view.Allow(sub, rpc.VerbRead, authz.Aria(r.ID))
		switch {
		case d.Allowed:
			out = append(out, r)
		case d.Unreadable():
			refused.count++
			refused.why = d.Reason()
		}
	}
	return out, refused
}

func (h *handlers) takenWith(id string) []string {
	if h.angelus.Policies == nil {
		return nil
	}
	seen := map[string]bool{id: true}
	var out []string
	add := func(ids []string) {
		for _, n := range ids {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	add(h.subtree(id))
	if rk, ok := h.angelus.Backend.(killReaper); ok {
		add(rk.DeleteSet(id))
	}
	return out
}

func (h *handlers) subtree(id string) []string {
	if h.angelus.Backend == nil {
		return nil
	}
	children := map[string][]string{}
	for _, n := range h.angelus.Backend.Nodes() {
		children[n.Parent] = append(children[n.Parent], n.ID)
	}
	var out []string
	queue := children[id]
	seen := map[string]bool{id: true}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		queue = append(queue, children[n]...)
	}
	return out
}

func (h *handlers) studiesAFigaro(id string) bool {
	b := h.angelus.Backend
	if b == nil {
		return false
	}
	n, ok := b.Node(id)
	return ok && n.Kind != "form"
}
