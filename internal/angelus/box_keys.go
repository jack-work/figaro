package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/methods"
	"github.com/jack-work/figaro/internal/store"
)

var boxWritable = []string{
	"system.cwd",
	"system.sandbox",
	"system.provider",
	"system.model",
	"system.max_tokens",
	"system.max_context_tokens",
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
	"system.credo",
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

var boxHeldKeys = []struct{ key, why string }{
	{"system.environment", "it names a host directory the daemon writes wire logs to"},
	{decide.BaseURLKey, "it is a URL the daemon sends a provider credential to"},
	{methods.FormKey, "a method names keys the daemon writes for the aria, past every check on a caller"},
	{store.StudiesKey, "it names forms the aria reads; study checks read on each"},
	{"target-aria", "it makes a form a role the daemon resolves and keeps alive; cast checks its target"},
	{store.SystemReapedKey, "it names the root the purge groups this aria under; kill writes it"},
	{store.SystemReapedUntilKey, "it schedules a purge; kill writes it"},
}

func under(path, key string) bool {
	return path == key || strings.HasPrefix(path, key+".") || strings.HasPrefix(path, key+"[")
}

func boxRefusal(path string) (string, bool) {
	for _, k := range boxHeldKeys {
		if under(path, k.key) {
			return k.key + ": " + k.why, true
		}
	}
	if !strings.HasPrefix(path, "system.") {
		return "", false
	}
	for _, k := range boxWritable {
		if under(path, k) {
			return "", false
		}
	}
	return path + ": a box writes only the system keys it is listed for", true
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

func refuseBoxKeys(ctx context.Context, before form.Snapshot, patch *form.Patch) error {
	if patch == nil || patch.IsIdentity() {
		return nil
	}
	box, ok := callerBox(ctx)
	if !ok {
		return nil
	}
	for _, p := range changedPaths(before, before.Apply(*patch)) {
		if why, refused := boxRefusal(p); refused {
			return fmt.Errorf("box %s may not set %s", box, why)
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
			if open := openKeys(req.Form.Context); len(open) > 0 {
				snap, err := h.angelus.Backend.FormState(id)
				if err != nil {
					return err
				}
				if err := refuseBoxKeys(ctx, snap, ptr(form.FromMap(open).AsPatch())); err != nil {
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
	return refuseBoxKeys(ctx, snap, patch)
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
