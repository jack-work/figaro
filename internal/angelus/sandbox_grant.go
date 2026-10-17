package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/sandbox"
	"github.com/jack-work/jkauthz"
)

func (h *handlers) subject(ctx context.Context) (jkauthz.Subject, error) {
	p := authz.PrincipalOf(ctx)
	if p.Kind == rpc.PrincipalUser && p.Node == "" && h.angelus.Peercred != nil {
		if uid, err := strconv.ParseUint(p.ID, 10, 32); err == nil {
			return h.angelus.Peercred.Subject(uint32(uid))
		}
	}
	if p.Kind == "" || p.ID == "" {
		return jkauthz.Subject{}, nil
	}
	return jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.Kind(p.Kind), ID: p.ID, Node: p.Node}}, nil
}

func (h *handlers) grantSandbox(ctx context.Context, snap form.Snapshot, cwdSet bool) (form.Patch, error) {
	policies := h.angelus.Policies
	if policies == nil {
		return form.Patch{}, nil
	}
	sub, err := h.subject(ctx)
	if err != nil {
		return form.Patch{}, err
	}
	kind := ""
	if s := snap.Lookup("system.sandbox"); s != nil {
		kind = *s
	}
	if d := policies.Allow(sub, rpc.VerbUse, authz.Sandbox(kind)); !d.Allowed {
		if sub.Principal.Kind != jkauthz.KindSandbox || sandbox.HostPath(sub.Principal.ID) {
			return form.Patch{}, fmt.Errorf("sandbox %q: %s", sandboxName(kind), d.Reason())
		}
		pin, _ := json.Marshal(sub.Principal.ID)
		return form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": pin}, nil), nil
	}
	if cwdSet && sandbox.HostPath(kind) {
		if d := policies.Allow(sub, rpc.VerbUse, authz.Sandbox("local")); !d.Allowed {
			return form.Patch{}, fmt.Errorf("system.cwd under sandbox %q is a path on this host, which needs use on sandbox:local: %s", sandboxName(kind), d.Reason())
		}
	}
	return form.Patch{}, nil
}

func sandboxName(kind string) string {
	if kind == "" {
		return "local"
	}
	return kind
}

func (h *handlers) grantSandboxOn(ctx context.Context, id string, cwdSet bool) error {
	if h.angelus.Policies == nil {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	pin, err := h.grantSandbox(ctx, snap, cwdSet)
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
	if err := p.Reload(loaded.PolicyPath()); err != nil {
		slog.Error("authz: policy reload failed; the previous policy stays in force", "err", err)
		return err
	}
	slog.Info("authz: policy reloaded", "path", loaded.PolicyPath())
	return nil
}
