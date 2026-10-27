package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/methods"
	"github.com/jack-work/figaro/internal/store"
)

var boxHeldKeys = []struct{ key, why string }{
	{"system.environment", "it names a host directory the daemon writes wire logs to"},
	{decide.BaseURLKey, "it is a URL the daemon sends a provider credential to"},
	{methods.FormKey, "a method names keys the daemon writes for the aria, past every check on a caller"},
	{store.StudiesKey, "it names forms the aria reads; study checks read on each"},
	{"target-aria", "it makes a form a role the daemon resolves and keeps alive; cast checks its target"},
	{store.SystemReapedKey, "it names the root the purge groups this aria under; kill writes it"},
	{store.SystemReapedUntilKey, "it schedules a purge; kill writes it"},
}

func callerBox(ctx context.Context) (string, bool) {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return "", false
	}
	return authz.BoxOf(sub.Principal)
}

func refuseBoxKeys(ctx context.Context, before form.Snapshot, patch *form.Patch) error {
	if patch == nil || patch.IsIdentity() {
		return nil
	}
	box, ok := callerBox(ctx)
	if !ok {
		return nil
	}
	after := before.Apply(*patch)
	for _, k := range boxHeldKeys {
		was, _ := before.Get(k.key)
		now, _ := after.Get(k.key)
		if !bytes.Equal(was, now) {
			return fmt.Errorf("box %s may not set %s: %s", box, k.key, k.why)
		}
	}
	return nil
}

func (h *handlers) refuseBoxPatch(ctx context.Context, method, id string, params json.RawMessage) error {
	if _, ok := callerBox(ctx); !ok {
		return nil
	}
	var patch *form.Patch
	switch method {
	case rpc.MethodSet:
		var req rpc.SetRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return err
		}
		patch = &req.Patch
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return err
		}
		if req.Form != nil {
			patch = req.Form.Patch
		}
	case rpc.MethodCast:
		var req rpc.CastRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return err
		}
		patch = req.RolePatch
	default:
		return nil
	}
	if patch == nil || patch.IsIdentity() {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	return refuseBoxKeys(ctx, snap, patch)
}

func hostCwdOf(ctx context.Context, requested string) string {
	if _, ok := callerBox(ctx); ok {
		return ""
	}
	return requested
}
