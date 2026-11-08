package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/methods"
	"github.com/jack-work/figaro/internal/store"
)

var boxWritable = []string{
	"system.cwd",
	"system.sandbox",
	"system.context_tier",
	"system.thinking_budget",
	"system.thinking_effort",
	"system.reasoning_context",
	"system.reasoning_summary",
	"system.verbosity",
	"system.temperature",
	"system.top_p",
	"system.parallel_tool_calls",
	"system.cache_control",
	"system.cache_markers",
	"system.stream_request_body",
	"system.reminder_renderer",
	"system.tags",
	"system.retired",
	"system.study_incantation",
	"system.fork_incantation",
	"system.delta_bytes",
	"system.delta_key_bytes",
	decide.ProviderKey,
	decide.ModelKey,
	decide.APIKey,
}

var boxRetires = []string{store.SystemRetiredKey}

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

func changedPaths(before, after form.Snapshot) []string {
	was := map[string]json.RawMessage{}
	for k, v := range before.All() {
		was[k] = v
	}
	var out []string
	for k, v := range after.All() {
		if old, ok := was[k]; !ok || !bytes.Equal(old, v) {
			out = append(out, k)
		}
		delete(was, k)
	}
	for k := range was {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type callerKeys struct {
	owns  bool
	boxed bool
	box   string
}

func (h *handlers) callerKeysOf(ctx context.Context) callerKeys {
	box, boxed := callerBox(ctx)
	return callerKeys{owns: h.callerOwns(ctx), boxed: boxed, box: box}
}

func (c callerKeys) deny() []rpc.KeyDeny {
	var out []rpc.KeyDeny
	for _, k := range form.SystemManagedKeys() {
		out = append(out, rpc.KeyDeny{Key: k, Exact: true, Kind: rpc.DenySystemManaged,
			Reason: "written by the harness, not by hand"})
	}
	if !c.owns {
		for _, k := range form.OwnerHeldKeys() {
			if c.boxed && slices.Contains(boxRetires, k) {
				continue
			}
			out = append(out, rpc.KeyDeny{Key: k, Kind: rpc.DenyOwnerHeld,
				Reason: "owner-held: only the owner sets it"})
		}
	}
	if !c.boxed {
		return out
	}
	for _, k := range boxHeldKeys {
		out = append(out, rpc.KeyDeny{Key: k.key, Kind: rpc.DenyBoxHeld, Reason: k.why})
	}
	return append(out, rpc.KeyDeny{Key: "system.", Below: true, Except: boxWritable, Kind: rpc.DenyBoxUnlisted,
		Reason: "a box writes only the system keys it is listed for"})
}

func (c callerKeys) refusal(path string) error {
	for _, d := range c.deny() {
		if d.Kind == rpc.DenySystemManaged || !d.Denies(path) {
			continue
		}
		switch d.Kind {
		case rpc.DenyOwnerHeld:
			if c.boxed {
				return fmt.Errorf("box %s may not set %s: it is owner-held", c.box, d.Key)
			}
			return fmt.Errorf("%s is owner-held: only the owner sets it", d.Key)
		case rpc.DenyBoxHeld:
			return fmt.Errorf("box %s may not set %s: %s", c.box, d.Key, d.Reason)
		default:
			return fmt.Errorf("box %s may not set %s: %s", c.box, path, d.Reason)
		}
	}
	return nil
}

func (h *handlers) refuseCallerKeys(ctx context.Context, before form.Snapshot, patch *form.Patch) error {
	if patch == nil || patch.IsIdentity() {
		return nil
	}
	c := h.callerKeysOf(ctx)
	if c.owns && !c.boxed {
		return nil
	}
	for _, p := range changedPaths(before, before.Apply(*patch)) {
		if err := c.refusal(p); err != nil {
			return err
		}
	}
	return nil
}

func (h *handlers) formAccess(ctx context.Context, id string, params json.RawMessage) *rpc.FormAccess {
	if isCatalogID(id) {
		return &rpc.FormAccess{Mode: rpc.FormReadOnly}
	}
	var req rpc.FormRequest
	if len(params) > 0 {
		_ = json.Unmarshal(params, &req)
	}
	if req.Intrinsic != "" && req.Intrinsic != figaro.IntrinsicState {
		return &rpc.FormAccess{Mode: rpc.FormReadOnly}
	}
	return &rpc.FormAccess{Mode: rpc.FormWritable, Deny: h.callerKeysOf(ctx).deny()}
}

func (h *handlers) callerOwns(ctx context.Context) bool {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return false
	}
	return h.passesUnpoliced(ctx, sub)
}

func (h *handlers) refuseCallerPatch(ctx context.Context, method, id string, params json.RawMessage) error {
	if _, boxed := callerBox(ctx); !boxed && h.callerOwns(ctx) {
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
			if open := openKeys(req.Form.Context); len(open) > 0 {
				snap, err := h.angelus.Backend.FormState(id)
				if err != nil {
					return err
				}
				if err := h.refuseCallerKeys(ctx, snap, ptr(form.FromMap(open).AsPatch())); err != nil {
					return err
				}
			}
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
	return h.refuseCallerKeys(ctx, snap, patch)
}

func openKeys(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		if !strings.HasPrefix(k, "system.") && k != "system" {
			out[k] = v
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func hostCwdOf(ctx context.Context, requested string) string {
	if _, ok := callerBox(ctx); ok {
		return ""
	}
	return requested
}
