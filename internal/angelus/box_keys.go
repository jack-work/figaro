package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
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
	{poolNodeKey, "it names the node a pool's leases are placed under; the daemon mints it"},
	{store.ReapedKey, "it names the root the purge groups this aria under; kill writes it"},
	{store.ReapedUntilKey, "it schedules a purge; kill writes it"},
	{store.ReapedByKey, "it names the run gc --undo restores; gc writes it"},
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

func writtenPaths(before form.Snapshot, patch form.Patch, after form.Snapshot) []string {
	out := changedPaths(before, after)
	if e, ok := patch.Entry(store.SystemRetiredKey); ok && e.IsRemoval() && store.RetiredIn(before) && !slices.Contains(out, store.SystemRetiredKey) {
		out = append(out, store.SystemRetiredKey)
		sort.Strings(out)
	}
	return out
}

type callerKeys struct {
	owns    bool
	boxed   bool
	box     string
	lineage bool
	heir    bool
	tool    string
}

func (h *handlers) callerKeysOf(ctx context.Context) callerKeys {
	box, boxed := callerBox(ctx)
	owns := h.callerOwns(ctx)
	tool, _ := authz.ToolOf(ctx)
	if tool == "" && owns && !boxed && !keyGrantsWaived(ctx) {
		tool = authz.FigaroOf(ctx)
	}
	return callerKeys{owns: owns && tool == "", boxed: boxed, box: box, lineage: h.protectsByLineage(), tool: tool}
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
	of    string
	self  bool
	birth bool
}

func birthUnder(parent string) lineage { return lineage{of: parent, self: true, birth: true} }

func rootBirth() lineage { return lineage{birth: true} }

func (h *handlers) refuseProtected(ctx context.Context, c callerKeys, line lineage, key string, current form.Snapshot) error {
	if c.owns && !c.boxed {
		return nil
	}
	if key == "" {
		key = "a protected key"
	}
	if c.tool != "" {
		if line.of == "" || !slices.Contains(h.ancestorsOf(line), c.tool) {
			return fmt.Errorf("figaro %s may not set %s: it is protected, and %s is no ancestor of the aria", c.tool, key, c.tool)
		}
		return nil
	}
	if !c.boxed {
		return fmt.Errorf("%s is protected: only the owner or an ancestor figaro sets it, and a figaro is known only through its box door, its tool cgroup or its label on the local door", key)
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
	if p := h.angelus.Policies; p != nil && f != "" && (c.tool == f || c.boxed && h.sandboxOf(f) == c.box) {
		target, parent := line.of, ""
		if line.self {
			target, parent = "", line.of
		}
		if err := p.HoldsGrants(f, want, target, parent); err != nil {
			return fmt.Errorf("%s: %w", form.ScopesKey, err)
		}
		return nil
	}
	if len(want) == 0 {
		return nil
	}
	return fmt.Errorf("%s: the writer does not hold %q, and a figaro gives only what it holds", form.ScopesKey, want[0])
}

func claimsUnheld(ctx context.Context) bool {
	return authz.FigaroOf(ctx) == "" && authz.Claimed(ctx) && authz.DoorOf(ctx).Trusted()
}

func (h *handlers) refuseKeysBeyondGrants(ctx context.Context, line lineage, before form.Snapshot, patch form.Patch, after form.Snapshot) error {
	if claimsUnheld(ctx) && (keyChanged(before, after, store.SystemRetiredKey) || keyChanged(before, after, form.RetiredKey)) {
		return errors.New("retire: the caller names a figaro this daemon does not hold")
	}
	f, p := authz.FigaroOf(ctx), h.angelus.Policies
	if f == "" || p == nil || line.of == "" || line.self {
		return nil
	}
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return err
	}
	if keyChanged(before, after, store.SystemRetiredKey) || keyChanged(before, after, form.RetiredKey) {
		if err := p.TopologyReach(sub, f, rpc.VerbSet, line.of); err != nil {
			return err
		}
	}
	if keyGrantsWaived(ctx) {
		return nil
	}
	view := p.Figaro(f)
	for _, key := range writtenPaths(before, patch, after) {
		if judgedByLifetime(f, line, key, after) {
			continue
		}
		verb := rpc.VerbSet
		if !after.Has(key) {
			verb = rpc.VerbDelete
		}
		if !view.AllowsKey(sub, verb, authz.Aria(line.of), key) {
			return fmt.Errorf("figaro %s may not %s %s on %s: its scopes do not reach that key", f, keyVerbName(verb), key, line.of)
		}
	}
	return nil
}

func judgedByLifetime(figaro string, line lineage, key string, after form.Snapshot) bool {
	switch key {
	case store.SystemTTLKey:
		return true
	case store.SystemRetiredKey:
		return line.of == figaro && after.Has(key)
	}
	return false
}

func keyVerbName(v rpc.Verb) string {
	if v == rpc.VerbSet {
		return "write"
	}
	return string(v)
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
		out = append(out, rpc.KeyDeny{Key: k, Exact: k != form.RuntimeRoot, Kind: rpc.DenySystemManaged,
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
			if c.tool != "" && (d.Key == store.SystemTTLKey || d.Key == store.SystemRetiredKey) {
				return fmt.Errorf("figaro %s may not set %s that way: a figaro's tools only shorten a lifetime (a finite ttl no later than the current one, or retiring), on themselves, their descendants, a figaro they create or one a written grant lets them retire; 0, a removal, a later deadline or unretiring is the owner's", c.tool, d.Key)
			}
			if c.tool != "" {
				return fmt.Errorf("figaro %s may not set %s: it is owner-held, and a figaro's tools are never the owner", c.tool, d.Key)
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

var reapKeys = form.RuntimeDrops(store.ReapedKey, store.ReapedUntilKey, store.ReapedByKey)

func refuseRuntimeKeys(before form.Snapshot, patch form.Patch) error {
	after := before.Apply(patch)
	for _, p := range changedPaths(before, after) {
		if form.WriterOf(p) == form.WriterRuntime {
			return fmt.Errorf("a caller may not set %s: only the daemon writes it", p)
		}
	}
	return nil
}

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

func touchedPaths(patch form.Patch) []string {
	entries := patch.Entries()
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}

func refuseMisshapenKeys(after form.Snapshot, patch form.Patch) error {
	faults := form.TouchedShapeFaults(after, touchedPaths(patch))
	if len(faults) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", faults[0].Path, faults[0].Err)
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

type waivedKeyGrants struct{}

func keyGrantsWaived(ctx context.Context) bool {
	waived, _ := ctx.Value(waivedKeyGrants{}).(bool)
	return waived
}

func (h *handlers) refuseCallerKeys(ctx context.Context, line lineage, before form.Snapshot, patch *form.Patch) error {
	err := h.checkCallerKeys(ctx, line, before, patch)
	p := h.angelus.Policies
	if err == nil || p.Enforces(authz.KeyGrants) {
		return err
	}
	if pre := h.checkCallerKeys(context.WithValue(ctx, waivedKeyGrants{}, true), line, before, patch); pre != nil {
		return pre
	}
	p.WouldRefuse(authz.KeyGrants, err.Error())
	return nil
}

func (h *handlers) checkCallerKeys(ctx context.Context, line lineage, before form.Snapshot, patch *form.Patch) error {
	if patch == nil || patch.IsIdentity() {
		return nil
	}
	if err := refuseReapKeys(before, *patch); err != nil {
		return err
	}
	if err := refuseRuntimeKeys(before, *patch); err != nil {
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
	if _, _, err := form.FanoutMaxChildren(after); err != nil {
		return err
	}
	if _, err := form.FanoutMint(after); err != nil {
		return err
	}
	if err := refuseMisshapenKeys(after, *patch); err != nil {
		return err
	}
	if keyChanged(before, after, form.ChildPolicyKey) {
		if err := form.CheckChildPolicyFanout(after); err != nil {
			return err
		}
		if c := h.callerKeysOf(ctx); !(c.owns && !c.boxed) {
			if keys := form.ChildPolicyOwnerKeysChanged(before, after); len(keys) > 0 {
				return fmt.Errorf("%s: %s is owner-held: only the owner puts it in a child policy", form.ChildPolicyKey, keys[0])
			}
		}
	}
	if err := h.refuseKeysBeyondGrants(ctx, line, before, *patch, after); err != nil {
		return err
	}
	c := h.callerKeysOf(ctx)
	if c.owns && !c.boxed {
		if err := h.refuseFanoutByLabelAlone(ctx, line, before, after); err != nil {
			return err
		}
		return h.refuseMintByLabelAlone(ctx, line, before, after)
	}
	for _, p := range writtenPaths(before, *patch, after) {
		if k, ok := form.Protected(p); ok && c.lineage {
			if err := h.refuseProtected(ctx, c, line, k, before); err != nil {
				return err
			}
			continue
		}
		if retentionGranted(ctx, before, p) {
			continue
		}
		if h.toolShortensLifetime(ctx, c, line, before, after, p) {
			continue
		}
		if err := c.refusal(p); err != nil {
			return err
		}
	}
	if err := h.refuseScopesBeyondWriter(ctx, c, line, before, after); err != nil {
		return err
	}
	if err := h.refuseFanoutBeyondGranter(ctx, before, after); err != nil {
		return err
	}
	return h.refuseMintBeyondGranter(ctx, before, after)
}

func keyChanged(before, after form.Snapshot, key string) bool {
	was, wasOK := before.Get(key)
	now, nowOK := after.Get(key)
	return wasOK != nowOK || !bytes.Equal(was, now)
}

func (h *handlers) refuseMintByLabelAlone(ctx context.Context, line lineage, before, after form.Snapshot) error {
	f := authz.FigaroOf(ctx)
	if !keyChanged(before, after, form.FanoutMintKey) {
		return nil
	}
	if f == "" {
		if claimsUnheld(ctx) {
			return fmt.Errorf("%s is protected, and the caller names a figaro this daemon does not hold", form.FanoutMintKey)
		}
		return nil
	}
	if line.of == "" || !slices.Contains(h.ancestorsOf(line), f) {
		return fmt.Errorf("%s is protected, and figaro %s is no ancestor of the aria", form.FanoutMintKey, f)
	}
	return h.refuseMintBeyondGranter(ctx, before, after)
}

func (h *handlers) refuseMintBeyondGranter(ctx context.Context, before, after form.Snapshot) error {
	if !keyChanged(before, after, form.FanoutMintKey) {
		return nil
	}
	if granted, _ := form.FanoutMint(after); !granted {
		return nil
	}
	f := authz.FigaroOf(ctx)
	if f == "" {
		return fmt.Errorf("%s: only the owner or an ancestor figaro grants it", form.FanoutMintKey)
	}
	own, err := h.angelus.mayMint(f)
	if err != nil {
		return fmt.Errorf("%s: reading %s's own: %w", form.FanoutMintKey, f, err)
	}
	if !own {
		return fmt.Errorf("%s: %s does not hold mint itself, so it cannot grant it", form.FanoutMintKey, f)
	}
	return nil
}

func (h *handlers) refuseFanoutByLabelAlone(ctx context.Context, line lineage, before, after form.Snapshot) error {
	f := authz.FigaroOf(ctx)
	if f == "" && !claimsUnheld(ctx) {
		return nil
	}
	was, wasOK := before.Get(form.FanoutMaxChildrenKey)
	now, nowOK := after.Get(form.FanoutMaxChildrenKey)
	if nowOK && bytes.Equal(was, now) {
		return nil
	}
	if !nowOK && !wasOK {
		return nil
	}
	if f == "" {
		return fmt.Errorf("%s is protected, and the caller names a figaro this daemon does not hold", form.FanoutMaxChildrenKey)
	}
	if line.of == "" || !slices.Contains(h.ancestorsOf(line), f) {
		return fmt.Errorf("%s is protected, and figaro %s is no ancestor of the aria", form.FanoutMaxChildrenKey, f)
	}
	return h.refuseFanoutBeyondGranter(ctx, before, after)
}

func (h *handlers) refuseFanoutBeyondGranter(ctx context.Context, before, after form.Snapshot) error {
	was, wasOK := before.Get(form.FanoutMaxChildrenKey)
	now, nowOK := after.Get(form.FanoutMaxChildrenKey)
	if nowOK && bytes.Equal(was, now) {
		return nil
	}
	if !nowOK && !wasOK {
		return nil
	}
	f := authz.FigaroOf(ctx)
	if f == "" {
		return fmt.Errorf("%s: only the owner or an ancestor figaro grants it", form.FanoutMaxChildrenKey)
	}
	own, ok, gerr := h.angelus.fanoutLimit(f)
	if gerr != nil {
		return fmt.Errorf("%s: reading %s's own limit: %w", form.FanoutMaxChildrenKey, f, gerr)
	}
	if !nowOK {
		if ok {
			return fmt.Errorf("%s: %s grants at most its own max_children (%d); removing it grants unlimited", form.FanoutMaxChildrenKey, f, own)
		}
		return nil
	}
	n, _, err := form.FanoutMaxChildren(after)
	if err != nil {
		return err
	}
	if ok && n > own {
		return fmt.Errorf("%s: %s grants at most its own max_children (%d), not %d", form.FanoutMaxChildrenKey, f, own, n)
	}
	return nil
}

func retentionGranted(ctx context.Context, before form.Snapshot, path string) bool {
	if !strings.HasPrefix(path, store.KeyRetention+".") || path == store.KeyRetentionWriters || strings.HasPrefix(path, store.KeyRetentionWriters+".") || strings.HasPrefix(path, store.KeyRetentionWriters+"[") {
		return false
	}
	writers := store.RetentionWritersOf(before)
	if len(writers) == 0 {
		return false
	}
	if fig := authz.FigaroOf(ctx); fig != "" && slices.Contains(writers, fig) {
		return true
	}
	sub, err := authz.SubjectOf(ctx)
	return err == nil && slices.Contains(writers, sub.Principal.String())
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
	if _, tool := authz.ToolOf(ctx); tool {
		return h.callerIsOwner(ctx)
	}
	if h.angelus.Policies == nil {
		return h.passesUnpoliced(ctx, sub)
	}
	return authz.DoorOf(ctx) == authz.DoorInProcess || authz.IsOwner(h.angelus.owner(), sub)
}

func ownerKeyRulesSkip(params json.RawMessage) bool {
	return !bytes.Contains(params, []byte("reaped")) && !bytes.Contains(params, []byte("retired")) && !bytes.Contains(params, []byte("retirement")) && !bytes.Contains(params, []byte("grants")) && !bytes.Contains(params, []byte("scopes")) && !bytes.Contains(params, []byte("fanout")) && !bytes.Contains(params, []byte("child_policy")) && !bytes.ContainsRune(params, '\\')
}

func (h *handlers) refuseCallerPatch(ctx context.Context, method, id string, params json.RawMessage) error {
	c := h.callerKeysOf(ctx)
	owner := c.owns && !c.boxed
	shapesOnly := owner && ownerKeyRulesSkip(params)
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
			if open := contextWrites(req.Form.Context); len(open) > 0 {
				snap, err := h.angelus.Backend.FormState(id)
				if err != nil {
					return err
				}
				openPatch := form.FromMap(open).AsPatch()
				if owner {
					if err := refuseMisshapenKeys(snap.Apply(openPatch), openPatch); err != nil {
						return err
					}
				} else if err := h.refuseCallerKeys(ctx, lineage{of: id}, snap, &openPatch); err != nil {
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
	if snap, err = h.listedOutfitKeys(ctx, snap, requestOutfits(ctx)); err != nil {
		return err
	}
	if shapesOnly {
		if err := refuseRuntimeKeys(snap, *patch); err != nil {
			return err
		}
		return refuseMisshapenKeys(snap.Apply(*patch), *patch)
	}
	line := lineage{of: id}
	if method == rpc.MethodCast {
		line = lineage{}
	}
	return h.refuseCallerKeys(ctx, line, snap, patch)
}

func contextWrites(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		if !form.HarnessKey(k) && !form.Ephemeral(k) {
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
	eff, held, err := p.Effective(id)
	if !held {
		return fmt.Errorf("%s is not held here, so the narrowing it leaves cannot be carried", id)
	}
	if err != nil {
		return err
	}
	alone, err := p.EffectiveAlone(id)
	if err != nil {
		return err
	}
	if p.Covers(eff, alone) {
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

func (h *handlers) toolShortensLifetime(ctx context.Context, c callerKeys, line lineage, before, after form.Snapshot, path string) bool {
	if c.tool == "" {
		return false
	}
	switch path {
	case store.SystemTTLKey:
		if !ttlShortened(before, after) {
			return false
		}
	case store.SystemRetiredKey:
		raw, ok := after.Get(path)
		if !ok || line.birth || store.RetiredReason(lifetimeText(raw)) == "" {
			return false
		}
	default:
		return false
	}
	if line.birth {
		return true
	}
	return line.of != "" && (line.of == c.tool || slices.Contains(h.ancestorsOf(line), c.tool) || h.lifetimeGranted(ctx, c.tool, line.of, path))
}

func (h *handlers) lifetimeGranted(ctx context.Context, figaro, target, key string) bool {
	p := h.angelus.Policies
	sub, err := authz.SubjectOf(ctx)
	return p != nil && err == nil && p.ReachGranted(sub, figaro, rpc.VerbSet, target) && p.Figaro(figaro).AllowsKey(sub, rpc.VerbSet, authz.Aria(target), key)
}

func ttlShortened(before, after form.Snapshot) bool {
	raw, ok := after.Get(store.SystemTTLKey)
	if !ok {
		return false
	}
	now, err := store.ParseTTL(lifetimeText(raw))
	if err != nil || now <= 0 {
		return false
	}
	was, ok := before.Get(store.SystemTTLKey)
	if !ok {
		return true
	}
	old, err := store.ParseTTL(lifetimeText(was))
	if err != nil {
		return false
	}
	return old <= 0 || now <= old
}

func lifetimeText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b)
	}
	return string(raw)
}
