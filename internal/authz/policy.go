package authz

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

type ResourceKind uint8

const (
	ResourceDaemon ResourceKind = iota
	ResourceAria
	ResourceSandbox
	ResourceTroupe
)

type Resource struct {
	Kind ResourceKind
	ID   string
}

func Aria(id string) Resource { return Resource{Kind: ResourceAria, ID: id} }

func Sandbox(name string) Resource {
	if name == "" {
		name = "local"
	}
	return Resource{Kind: ResourceSandbox, ID: name}
}

func Troupe(name string) Resource { return Resource{Kind: ResourceTroupe, ID: name} }

var Daemon = Resource{}

type Facts interface {
	Owner(aria string) (jkauthz.Principal, bool)
	DescendsFrom(aria, root string) bool
	RoleTarget(role string) string
}

type selectors struct{ facts Facts }

const (
	selAria    = "aria:"
	selRole    = "role:@"
	selOwner   = "owner:self"
	selLineage = "lineage:"
	selSandbox = "sandbox:"
	selTroupe  = "troupe:"
)

func (selectors) Validate(sel string) error {
	if sel == selOwner {
		return nil
	}
	for _, prefix := range []string{selAria, selRole, selLineage, selSandbox, selTroupe} {
		if rest, ok := strings.CutPrefix(sel, prefix); ok {
			if rest == "" {
				return fmt.Errorf("%s names nothing", prefix)
			}
			return nil
		}
	}
	return fmt.Errorf("want aria:<id>, role:@<name>, owner:self, lineage:<id>, sandbox:<name> or troupe:<name>")
}

func (m selectors) Match(sel string, s jkauthz.Subject, r Resource) bool {
	switch r.Kind {
	case ResourceSandbox:
		name, ok := strings.CutPrefix(sel, selSandbox)
		return ok && name == r.ID
	case ResourceTroupe:
		name, ok := strings.CutPrefix(sel, selTroupe)
		return ok && name == r.ID
	}
	if r.Kind != ResourceAria {
		return false
	}
	switch {
	case sel == selOwner:
		if m.facts == nil {
			return false
		}
		owner, ok := m.facts.Owner(r.ID)
		return ok && owner == s.Principal
	case strings.HasPrefix(sel, selAria):
		return sel[len(selAria):] == r.ID
	case strings.HasPrefix(sel, selRole):
		return m.facts != nil && m.facts.RoleTarget(sel[len("role:"):]) == r.ID
	case strings.HasPrefix(sel, selLineage):
		root := sel[len(selLineage):]
		return root == r.ID || (m.facts != nil && m.facts.DescendsFrom(r.ID, root))
	}
	return false
}

func Schema(facts Facts) jkauthz.Schema[Resource] {
	verbs := make([]string, 0, len(rpc.Verbs()))
	for _, v := range rpc.Verbs() {
		verbs = append(verbs, string(v))
	}
	return jkauthz.Schema[Resource]{Verbs: verbs, Selectors: selectors{facts: facts}}
}

func DefaultPolicy(self jkauthz.Principal) string {
	return fmt.Sprintf(`
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "owner"
principals = [%q]
`, self.String())
}

type Policies struct {
	mu     sync.Mutex
	store  *jkauthz.Store[Resource]
	self   jkauthz.Principal
	path   string
	scopes []string
	troupe atomic.Pointer[troupeLayer]
	schema jkauthz.Schema[Resource]
}

type troupeLayer struct {
	name   string
	as     jkauthz.Subject
	policy *jkauthz.Policy[Resource]
	err    error
}

func NewPolicies(self jkauthz.Principal, facts Facts) *Policies {
	schema := Schema(facts)
	return &Policies{store: jkauthz.NewStore(schema), self: self, schema: schema}
}

func (p *Policies) SetIssuerScopes(scopes []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes = scopes
}

func (p *Policies) Reload(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = path
	if path == "" {
		return p.store.LoadBytes([]byte(DefaultPolicy(p.self)))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("policy %s: %w", path, err)
	}
	if err := p.store.LoadBytes(data); err != nil {
		return fmt.Errorf("policy %s: %w", path, err)
	}
	for _, name := range UnscopedNames(data) {
		slog.Warn("authz: policy names "+name+", which no issuer or edge produces: their principals and groups carry the issuer's scope", "policy", path, "write", ScopedSpelling(name, p.scopes))
	}
	return nil
}

func IssuerScopes(cfg config.AuthzConfig) []string {
	var out []string
	add := func(url string) {
		if scope, err := IssuerScope(url); err == nil && !slices.Contains(out, scope) {
			out = append(out, scope)
		}
	}
	add(cfg.Upstream.Issuer)
	for _, is := range cfg.Issuers {
		add(is.Issuer)
	}
	return out
}

func UnscopedNames(policy []byte) []string {
	var doc struct {
		Bind []struct {
			Principals []string `toml:"principals"`
			Groups     []string `toml:"groups"`
		} `toml:"bind"`
	}
	if _, err := toml.Decode(string(policy), &doc); err != nil {
		return nil
	}
	var out []string
	for _, b := range doc.Bind {
		for _, name := range b.Principals {
			pr, err := jkauthz.ParsePrincipal(name)
			if err == nil && pr.Kind == jkauthz.KindUser && pr.Node == "" && !strings.Contains(pr.ID, "@") && !isUID(pr.ID) {
				out = append(out, name)
			}
		}
		for _, g := range b.Groups {
			if !strings.Contains(g, "@") {
				out = append(out, "group "+g)
			}
		}
	}
	return out
}

func isUID(id string) bool {
	_, err := strconv.ParseUint(id, 10, 32)
	return err == nil
}

func ScopedSpelling(name string, scopes []string) string {
	scope := "<issuer host>"
	switch len(scopes) {
	case 0:
	case 1:
		scope = scopes[0]
	default:
		scope = "{" + strings.Join(scopes, ",") + "}"
	}
	if g, ok := strings.CutPrefix(name, "group "); ok {
		return "group " + g + "@" + scope
	}
	return name + "@" + scope
}

func (p *Policies) Path() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.path
}

type Verdict struct {
	jkauthz.Decision
	undelegated string
}

func (v Verdict) Reason() string {
	if v.undelegated != "" {
		return fmt.Sprintf("%s; troupe %s grants it, but this node does not delegate it to the troupe", v.Decision.Reason(), v.undelegated)
	}
	return v.Decision.Reason()
}

func (p *Policies) Allow(s jkauthz.Subject, verb rpc.Verb, r Resource) Verdict {
	local := p.store.Allow(s, string(verb), r)
	if local.Allowed {
		return Verdict{Decision: local}
	}
	t := p.troupe.Load()
	if t == nil || t.policy == nil || LocalOnly(verb, r) {
		return Verdict{Decision: local}
	}
	if !t.policy.Allow(s, string(verb), r).Allowed {
		return Verdict{Decision: local}
	}
	ceiling := p.store.Allow(t.as, string(verb), r)
	if !ceiling.Allowed {
		return Verdict{Decision: local, undelegated: t.name}
	}
	return Verdict{Decision: ceiling}
}

func LocalOnly(verb rpc.Verb, r Resource) bool {
	switch r.Kind {
	case ResourceSandbox:
		return r.ID == "local"
	case ResourceTroupe:
		return verb == rpc.VerbAdmin
	}
	return false
}

const KindTroupe jkauthz.Kind = "troupe"

func TroupePrincipal(name string) jkauthz.Principal {
	return jkauthz.Principal{Kind: KindTroupe, ID: name}
}

func (p *Policies) SetTroupe(name string, body []byte) error {
	if name == "" {
		p.troupe.Store(nil)
		return nil
	}
	layer := &troupeLayer{name: name, as: jkauthz.Subject{Principal: TroupePrincipal(name)}}
	if body == nil {
		p.troupe.Store(layer)
		return nil
	}
	pol, err := CompileTroupePolicy(body, p.schema)
	if err != nil {
		layer.err = err
		p.troupe.Store(layer)
		return err
	}
	layer.policy = pol
	p.troupe.Store(layer)
	return nil
}

func (p *Policies) TroupeLoadError() error {
	if t := p.troupe.Load(); t != nil {
		return t.err
	}
	return nil
}

func (p *Policies) TroupeName() string {
	if t := p.troupe.Load(); t != nil {
		return t.name
	}
	return ""
}

func (r Resource) String() string {
	switch r.Kind {
	case ResourceAria:
		return "aria:" + r.ID
	case ResourceSandbox:
		return "sandbox:" + r.ID
	case ResourceTroupe:
		return "troupe:" + r.ID
	}
	return "the daemon"
}
