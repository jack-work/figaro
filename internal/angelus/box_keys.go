package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	{successionPolicyKey, "it names the outfit the daemon dresses a successor in, as the owner"},
	{store.SystemReapedKey, "it names the root the purge groups this aria under; kill writes it"},
	{store.SystemReapedUntilKey, "it schedules a purge; kill writes it"},
	{store.SystemReapedByKey, "it names the run gc --undo restores; gc writes it"},
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
	owns    bool
	boxed   bool
	box     string
	lineage bool
	heir    bool
}

func (h *handlers) callerKeysOf(ctx context.Context) callerKeys {
	box, boxed := callerBox(ctx)
	return callerKeys{owns: h.callerOwns(ctx), boxed: boxed, box: box, lineage: h.protectsByLineage()}
}

func (h *handlers) callerKeysOn(ctx context.Context, line lineage) callerKeys {
	c := h.callerKeysOf(ctx)
	if c.lineage {
		snap, _ := h.angelus.Backend.FormState(line.of)
		c.heir = h.refuseProtected(ctx, c, line, "", snap) == nil
	}
	return c
}

func (h *handlers) protectsByLineage() bool {
	loaded, _ := h.settings()
	return loaded == nil || loaded.Config.Authz.ProtectsByLineage()
}

type lineage struct {
	of   string
	self bool
}

func birthUnder(parent string) lineage { return lineage{of: parent, self: true} }

func (h *handlers) refuseProtected(ctx context.Context, c callerKeys, line lineage, key string, current form.Snapshot) error {
	if c.owns && !c.boxed {
		return nil
	}
	if key == "" {
		key = "a protected key"
	}
	if !c.boxed {
		return fmt.Errorf("%s is protected: only the owner or an ancestor figaro sets it, and a figaro is known only through its box door", key)
	}
	f := authz.FigaroOf(ctx)
	if f == "" {
		return fmt.Errorf("box %s may not set %s: it is protected and the call names no figaro", c.box, key)
	}
	if got := h.sandboxOf(f); got != c.box {
		return fmt.Errorf("box %s may not set %s: the call claims figaro %s, which does not run in it", c.box, key, f)
	}
	if line.of == "" || !slices.Contains(h.ancestorsOf(line), f) {
		return fmt.Errorf("box %s may not set %s: it is protected, and figaro %s is no ancestor of the aria", c.box, key, f)
	}
	if s := current.Lookup("system.sandbox"); s == nil || *s != c.box {
		return fmt.Errorf("box %s may not set %s: the aria runs in sandbox %q, not in this box", c.box, key, sandboxName(deref(s)))
	}
	return nil
}

func changedScopes(before, after form.Snapshot) (json.RawMessage, bool, error) {
	was, _, _, _ := form.OwnScopes(before)
	now, _, ok, err := form.OwnScopes(after)
	if err != nil || !ok || bytes.Equal(was, now) {
		return nil, false, err
	}
	return now, true, nil
}

func checkScopesParse(before, after form.Snapshot) error {
	now, changed, err := changedScopes(before, after)
	if err != nil || !changed {
		return err
	}
	if _, err := authz.DecodeFigaroGrants(now); err != nil {
		return fmt.Errorf("%s: %w", form.ScopesKey, err)
	}
	return nil
}

func (h *handlers) refuseScopesBeyondWriter(ctx context.Context, c callerKeys, line lineage, before, after form.Snapshot) error {
	now, changed, err := changedScopes(before, after)
	if err != nil || !changed {
		return err
	}
	want, err := authz.DecodeFigaroGrants(now)
	if err != nil {
		return err
	}
	f := authz.FigaroOf(ctx)
	if p := h.angelus.Policies; p != nil && c.boxed && f != "" && h.sandboxOf(f) == c.box {
		if err := p.HoldsGrants(f, want, line.of); err != nil {
			return fmt.Errorf("%s: %w", form.ScopesKey, err)
		}
		return nil
	}
	if len(want) == 0 {
		return nil
	}
	return fmt.Errorf("%s: the writer does not hold %q, and a figaro gives only what it holds", form.ScopesKey, want[0])
}

func (h *handlers) ancestorsOf(line lineage) []string {
	chain, err := h.angelus.chainOf(line.of)
	if err != nil || len(chain) == 0 || chain[len(chain)-1].Node != line.of {
		return nil
	}
	if !line.self {
		chain = chain[:len(chain)-1]
	}
	out := make([]string, len(chain))
	for i, l := range chain {
		out[i] = l.Node
	}
	return out
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
	except := boxWritable
	if c.lineage && !(c.owns && !c.boxed) {
		if !c.heir {
			for _, k := range form.ProtectedKeys() {
				out = append(out, rpc.KeyDeny{Key: k, Kind: rpc.DenyProtected,
					Reason: "protected: only the owner or an ancestor figaro sets it"})
			}
		}
		except = append(slices.Clone(boxWritable), form.ProtectedKeys()...)
	}
	if !c.boxed {
		return out
	}
	for _, k := range boxHeldKeys {
		out = append(out, rpc.KeyDeny{Key: k.key, Kind: rpc.DenyBoxHeld, Reason: k.why})
	}
	return append(out, rpc.KeyDeny{Key: "system.", Below: true, Except: except, Kind: rpc.DenyBoxUnlisted,
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

var reapKeys = []string{store.SystemReapedKey, store.SystemReapedUntilKey, store.SystemReapedByKey}

func refuseReapKeys(before form.Snapshot, patch form.Patch) error {
	var after *form.Snapshot
	for _, e := range patch.Entries() {
		for _, k := range reapKeys {
			switch {
			case e.Key == k || strings.HasPrefix(e.Key, k+".") || strings.HasPrefix(e.Key, k+"["):
			case strings.HasPrefix(k, e.Key+"."):
				if after == nil {
					after = ptr(before.Apply(patch))
				}
				was, _ := before.Get(k)
				now, _ := after.Get(k)
				if bytes.Equal(was, now) {
					continue
				}
			default:
				continue
			}
			return fmt.Errorf("a caller may not set %s: only kill, gc and reaped restore write it", k)
		}
	}
	return nil
}

func refuseMalformedRetirement(before form.Snapshot, patch form.Patch) error {
	touched := false
	for _, e := range patch.Entries() {
		if e.Key == store.SystemRetirementKey || strings.HasPrefix(e.Key, store.SystemRetirementKey+".") || strings.HasPrefix(store.SystemRetirementKey, e.Key+".") {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	_, err := store.RetirementOf(before.Apply(patch))
	return err
}

func (h *handlers) refuseCallerKeys(ctx context.Context, line lineage, before form.Snapshot, patch *form.Patch) error {
	if patch == nil || patch.IsIdentity() {
		return nil
	}
	if err := refuseReapKeys(before, *patch); err != nil {
		return err
	}
	if err := refuseMalformedRetirement(before, *patch); err != nil {
		return err
	}
	after := before.Apply(*patch)
	if err := form.RefuseRetiredGrantsWrite(before, after); err != nil {
		return err
	}
	if err := form.CheckScopes(after); err != nil {
		return err
	}
	if err := checkScopesParse(before, after); err != nil {
		return err
	}
	c := h.callerKeysOf(ctx)
	if c.owns && !c.boxed {
		return nil
	}
	for _, p := range changedPaths(before, after) {
		if k, ok := form.Protected(p); ok && c.lineage {
			if err := h.refuseProtected(ctx, c, line, k, before); err != nil {
				return err
			}
			continue
		}
		if err := c.refusal(p); err != nil {
			return err
		}
	}
	return h.refuseScopesBeyondWriter(ctx, c, line, before, after)
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
	if h.allowed(ctx, rpc.VerbSet, authz.Aria(id)) != nil {
		return &rpc.FormAccess{Mode: rpc.FormReadOnly}
	}
	return &rpc.FormAccess{Mode: rpc.FormWritable, Deny: h.callerKeysOn(ctx, lineage{of: id}).deny()}
}

func (h *handlers) callerOwns(ctx context.Context) bool {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return false
	}
	return h.passesUnpoliced(ctx, sub)
}

func (h *handlers) refuseCallerPatch(ctx context.Context, method, id string, params json.RawMessage) error {
	_, boxed := callerBox(ctx)
	owner := !boxed && h.callerOwns(ctx)
	if owner && !bytes.Contains(params, []byte("reaped")) && !bytes.Contains(params, []byte("retirement")) && !bytes.Contains(params, []byte("grants")) && !bytes.Contains(params, []byte("scopes")) && !bytes.ContainsRune(params, '\\') {
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
			if open := openKeys(req.Form.Context); len(open) > 0 && !owner {
				snap, err := h.angelus.Backend.FormState(id)
				if err != nil {
					return err
				}
				if err := h.refuseCallerKeys(ctx, lineage{of: id}, snap, ptr(form.FromMap(open).AsPatch())); err != nil {
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
	line := lineage{of: id}
	if method == rpc.MethodCast {
		line = lineage{}
	}
	return h.refuseCallerKeys(ctx, line, snap, patch)
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func hostCwdOf(ctx context.Context, requested string) string {
	if _, ok := callerBox(ctx); ok {
		return ""
	}
	return requested
}

type detachCarrier interface {
	SetDetachCarry(func(ids []string) error)
}

func (h *handlers) carryBeforeDetach(ids []string) error {
	p := h.angelus.Policies
	if p == nil {
		return nil
	}
	for _, id := range ids {
		if n, ok := h.angelus.Backend.Node(id); !ok || n.Kind != store.KindConversation {
			continue
		}
		g, held := p.ExplainGrants(id)
		if !held || len(g.Unread) > 0 {
			return fmt.Errorf("%s would leave the figaro that narrows it, and its scopes cannot be read to carry that narrowing", id)
		}
		if err := h.carryNarrowing(id); err != nil {
			return fmt.Errorf("carrying the narrowing of %s: %w", id, err)
		}
	}
	return nil
}

func (h *handlers) carryNarrowing(id string) error {
	p := h.angelus.Policies
	if p == nil {
		return nil
	}
	eff, held := p.Effective(id)
	alone, _ := p.EffectiveAlone(id)
	if !held || p.Covers(eff, alone) {
		return nil
	}
	raw, err := json.Marshal(eff.Strings())
	if err != nil {
		return err
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	if _, err := h.angelus.Backend.ApplyForm(id, form.Build(snap, map[string]json.RawMessage{form.ScopesKey: raw}, []string{form.RetiredGrantsKey})); err != nil {
		return fmt.Errorf("writing the scopes its ancestors narrowed it to: %w", err)
	}
	slog.Info("scopes: a figaro leaving its ancestors carries their narrowing", "id", id, "scopes", eff.String())
	return nil
}
