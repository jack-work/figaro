package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

const DelegateHeader = "X-Figaro-On-Behalf-Of"

const maxDelegateName = 128

const delegatedMark = "\x00delegated by "

type DelegationError struct{ reason string }

func (e *DelegationError) Error() string { return "on-behalf-of refused: " + e.reason }

func refuseDelegation(format string, args ...any) error {
	return &DelegationError{reason: fmt.Sprintf(format, args...)}
}

type delegationTable map[jkauthz.Principal]map[jkauthz.Principal]bool

type Delegations struct {
	table atomic.Pointer[delegationTable]
}

func NewDelegations(cfgs []config.DelegationConfig) (*Delegations, error) {
	d := &Delegations{}
	return d, d.Reload(cfgs)
}

func (d *Delegations) users() delegationTable {
	if d == nil {
		return nil
	}
	if t := d.table.Load(); t != nil {
		return *t
	}
	return nil
}

func (d *Delegations) permits(service, user jkauthz.Principal) bool {
	return d.users()[service][user]
}

func (d *Delegations) Reload(cfgs []config.DelegationConfig) error {
	table, err := compileDelegations(cfgs)
	if err != nil {
		return err
	}
	d.table.Store(&table)
	return nil
}

func compileDelegations(cfgs []config.DelegationConfig) (delegationTable, error) {
	d := struct{ users delegationTable }{delegationTable{}}
	for i, c := range cfgs {
		svc, err := jkauthz.ParsePrincipal(c.Principal)
		if err != nil {
			return nil, fmt.Errorf("authz.delegation[%d].principal: %w", i, err)
		}
		scope, ok := scopeOfPrincipal(svc)
		if svc.Kind != KindService || svc.Node != "" || !ok {
			return nil, fmt.Errorf("authz.delegation[%d].principal = %q: want service:<client>@<issuer scope>, a service an issuer vouches for", i, c.Principal)
		}
		if _, dup := d.users[svc]; dup {
			return nil, fmt.Errorf("authz.delegation[%d]: %s is listed twice", i, c.Principal)
		}
		if len(c.Users) == 0 {
			return nil, fmt.Errorf("authz.delegation[%d]: %s lists no users, so it may act for nobody", i, c.Principal)
		}
		allowed := map[jkauthz.Principal]bool{}
		for _, text := range c.Users {
			u, err := jkauthz.ParsePrincipal(text)
			if err != nil {
				return nil, fmt.Errorf("authz.delegation[%d].users: %w", i, err)
			}
			if got, _ := scopeOfPrincipal(u); u.Kind != jkauthz.KindUser || u.Node != "" || got != scope {
				return nil, fmt.Errorf("authz.delegation[%d].users: %q is not a user of @%s; a service acts for users of the issuer that vouches for it", i, text, scope)
			}
			allowed[u] = true
		}
		d.users[svc] = allowed
	}
	return d.users, nil
}

func scopeOfPrincipal(p jkauthz.Principal) (string, bool) {
	at := strings.LastIndex(p.ID, "@")
	if at <= 0 || at == len(p.ID)-1 {
		return "", false
	}
	return p.ID[at+1:], true
}

func (d *Delegations) Empty() bool { return len(d.users()) == 0 }

type delegated struct {
	service jkauthz.Principal
	user    jkauthz.Principal
	name    string
	from    *Delegations
}

func (d *Delegations) Apply(ctx context.Context, name string) (context.Context, error) {
	c, _ := ctx.Value(callerKey{}).(caller)
	if c.session == nil && c.key == nil {
		return nil, refuseDelegation("only a caller admitted by a bearer may name the user it acts for")
	}
	if c.onBehalf != nil {
		return nil, refuseDelegation("this connection already acts for %s", c.onBehalf.user)
	}
	svc, err := SubjectOf(ctx)
	if err != nil {
		return nil, err
	}
	if d.Empty() {
		return nil, refuseDelegation("this node lists no service that may act for a user ([[authz.delegation]]), so %s acts only as itself: drop %s", svc.Principal, DelegateHeader)
	}
	allowed, listed := d.users()[svc.Principal]
	if !listed {
		return nil, refuseDelegation("%s is not listed to act for a user ([[authz.delegation]])", svc.Principal)
	}
	if err := checkDelegateName(name); err != nil {
		return nil, err
	}
	scope, _ := scopeOfPrincipal(svc.Principal)
	user := jkauthz.Principal{Kind: jkauthz.KindUser, ID: name + "@" + scope}
	if !allowed[user] {
		return nil, refuseDelegation("%s is not listed to act for %s", svc.Principal, user)
	}
	c.onBehalf = &delegated{service: svc.Principal, user: user, name: name, from: d}
	return context.WithValue(ctx, callerKey{}, c), nil
}

func checkDelegateName(name string) error {
	if name == "" {
		return refuseDelegation("%s names no user", DelegateHeader)
	}
	if len(name) > maxDelegateName || strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(":/@*", r)
	}) {
		return refuseDelegation("%s is not a user name", DelegateHeader)
	}
	return nil
}

func (d delegated) subject() jkauthz.Subject {
	return jkauthz.Subject{Principal: d.user, Groups: []string{delegatedMark + d.service.String()}}
}

func DelegatedVia(s jkauthz.Subject) (jkauthz.Principal, bool) {
	for _, g := range s.Groups {
		if text, ok := strings.CutPrefix(g, delegatedMark); ok {
			p, _ := jkauthz.ParsePrincipal(text)
			return p, true
		}
	}
	return jkauthz.Principal{}, false
}

func OnBehalfOf(ctx context.Context) (name string, ok bool) {
	c, _ := ctx.Value(callerKey{}).(caller)
	if c.onBehalf == nil {
		return "", false
	}
	return c.onBehalf.name, true
}

func delegable(verb rpc.Verb, r Resource) bool {
	switch verb {
	case rpc.VerbRead, rpc.VerbListen, rpc.VerbSend, rpc.VerbFork:
		return true
	case rpc.VerbSet:
		return r.Kind == ResourceAria && !reservedForm(r.ID)
	case rpc.VerbUse:
		return r.Kind == ResourceSandbox && r.ID == "local"
	}
	return false
}

func delegateRefusal(via, user jkauthz.Principal, verb rpc.Verb, r Resource) string {
	return fmt.Sprintf("%s acts for %s and may not %s on %s: a delegated caller may read, listen, send, fork, set plain keys on an aria or role form, and use sandbox:local", via, user, verb, r)
}

func (d delegated) refuses(method string, params json.RawMessage) error {
	verb, ok := rpc.VerbOf(method)
	if !ok {
		return fmt.Errorf("%s has no verb, so no policy can allow it", method)
	}
	if verb != rpc.VerbNone && method != rpc.MethodSet && !delegable(verb, Daemon) {
		return fmt.Errorf("%s: %s", method, delegateRefusal(d.service, d.user, verb, Daemon))
	}
	switch method {
	case rpc.MethodSet:
		var req rpc.SetRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return fmt.Errorf("%s: a delegated call's params must decode: %w", method, err)
		}
		if id := rpc.AriaOf(params); reservedForm(id) {
			return fmt.Errorf("%s: %s", method, delegateRefusal(d.service, d.user, verb, Aria(id)))
		}
		if len(req.Outfits) > 0 {
			return fmt.Errorf("%s: %s acts for %s and may not dress a form in outfits", method, d.service, d.user)
		}
		if err := d.refusesPatch(method, req.Patch); err != nil {
			return err
		}
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return fmt.Errorf("%s: a delegated call's params must decode: %w", method, err)
		}
		if req.Form == nil {
			return nil
		}
		if (req.Form.Patch != nil && !req.Form.Patch.IsIdentity()) || len(req.Form.Outfits) > 0 || len(writtenContext(req.Form.Context)) > 0 {
			return fmt.Errorf("%s: %s acts for %s and may send text only: its form input writes nothing (no patch, outfits or context keys)", method, d.service, d.user)
		}
	case rpc.MethodFork:
		var req rpc.ForkRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return fmt.Errorf("%s: a delegated call's params must decode: %w", method, err)
		}
		if len(req.Outfits) > 0 || req.Seat != "" || req.Abdicates {
			return fmt.Errorf("%s: %s acts for %s and may fork with a patch of plain keys only: no outfits, seat or abdication", method, d.service, d.user)
		}
		if req.Patch != nil {
			return d.refusesPatch(method, *req.Patch)
		}
	}
	return nil
}

const (
	maxDelegatedValueBytes = 64 << 10
	maxDelegatedPatchKeys  = 32
)

func reservedForm(id string) bool {
	switch id {
	case rpc.AriasFormID, rpc.GlobalAriasFormID, rpc.TroupeFormID, rpc.TopologyFormID, rpc.RecentFormID:
		return true
	}
	return false
}

func (d delegated) refusesPatch(method string, patch form.Patch) error {
	if patch.IsIdentity() {
		return nil
	}
	entries := patch.Entries()
	if len(entries) > maxDelegatedPatchKeys {
		return fmt.Errorf("%s: %s acts for %s and may write at most %d keys in one patch, this one writes %d", method, d.service, d.user, maxDelegatedPatchKeys, len(entries))
	}
	for _, e := range entries {
		if why := userUnwritable(e.Key); why != "" {
			return fmt.Errorf("%s: %s (%s acts for %s)", method, why, d.service, d.user)
		}
		if len(e.New) > maxDelegatedValueBytes {
			return fmt.Errorf("%s: %s acts for %s and may write a value of at most %d bytes, %s is %d", method, d.service, d.user, maxDelegatedValueBytes, e.Key, len(e.New))
		}
	}
	return nil
}

func userUnwritable(key string) string {
	if form.HarnessKey(key) {
		return key + " is daemon/owner-held: a delegated caller writes no system.* or runtime.* key"
	}
	if _, held := form.OwnerHeld(key); held {
		return key + " is owner-held"
	}
	if _, held := form.Protected(key); held {
		return key + " is protected lineage state"
	}
	if w := form.WriterOf(key); w != form.WriterSelf {
		return fmt.Sprintf("%s is written by its %s, not by a user", key, w)
	}
	return ""
}

func writtenContext(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		if !form.HarnessKey(k) && !form.Ephemeral(k) {
			out[k] = v
		}
	}
	return out
}

func pinnedToDelegate(ctx context.Context, params json.RawMessage) json.RawMessage {
	c, _ := ctx.Value(callerKey{}).(caller)
	if c.onBehalf == nil {
		if c.session == nil && c.key == nil {
			return params
		}
		return withoutLabel(params)
	}
	fields := map[string]json.RawMessage{}
	if len(params) > 0 && string(params) != "null" {
		if json.Unmarshal(params, &fields) != nil {
			return params
		}
	}
	delete(fields, rpc.CallerKey)
	label, _ := json.Marshal(rpc.CallerRef{Label: c.onBehalf.name})
	fields[rpc.CallerLabelKey] = label
	out, err := json.Marshal(fields)
	if err != nil {
		return params
	}
	return out
}

func withoutLabel(params json.RawMessage) json.RawMessage {
	ref := rpc.CallerRefOf(params)
	if ref == nil || ref.Label == "" {
		return params
	}
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(params, &fields) != nil {
		return params
	}
	if ref.Duke {
		kept, _ := json.Marshal(rpc.CallerRef{Duke: true})
		fields[rpc.CallerLabelKey] = kept
	} else {
		delete(fields, rpc.CallerLabelKey)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return params
	}
	return out
}
