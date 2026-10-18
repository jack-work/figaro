package authz

import (
	"fmt"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

type ResourceKind uint8

const (
	ResourceDaemon ResourceKind = iota
	ResourceAria
	ResourceSandbox
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
)

func (selectors) Validate(sel string) error {
	if sel == selOwner {
		return nil
	}
	for _, prefix := range []string{selAria, selRole, selLineage, selSandbox} {
		if rest, ok := strings.CutPrefix(sel, prefix); ok {
			if rest == "" {
				return fmt.Errorf("%s names nothing", prefix)
			}
			return nil
		}
	}
	return fmt.Errorf("want aria:<id>, role:@<name>, owner:self, lineage:<id> or sandbox:<name>")
}

func (m selectors) Match(sel string, s jkauthz.Subject, r Resource) bool {
	if r.Kind == ResourceSandbox {
		name, ok := strings.CutPrefix(sel, selSandbox)
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
	mu    sync.Mutex
	store *jkauthz.Store[Resource]
	self  jkauthz.Principal
	path  string
}

func NewPolicies(self jkauthz.Principal, facts Facts) *Policies {
	return &Policies{store: jkauthz.NewStore(Schema(facts)), self: self}
}

func (p *Policies) Reload(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = path
	if path == "" {
		return p.store.LoadBytes([]byte(DefaultPolicy(p.self)))
	}
	if err := p.store.Load(path); err != nil {
		return fmt.Errorf("policy %s: %w", path, err)
	}
	return nil
}

func (p *Policies) Path() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.path
}

func (p *Policies) Allow(s jkauthz.Subject, verb rpc.Verb, r Resource) jkauthz.Decision {
	return p.store.Allow(s, string(verb), r)
}

func (r Resource) String() string {
	switch r.Kind {
	case ResourceAria:
		return "aria:" + r.ID
	case ResourceSandbox:
		return "sandbox:" + r.ID
	}
	return "the daemon"
}
