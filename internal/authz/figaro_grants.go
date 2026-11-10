package authz

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

type FigaroFacts interface {
	Ancestry(aria string) ([]string, bool)
	OwnGrants(aria string) (raw json.RawMessage, present bool, err error)
}

var everyFigaroVerb = jkauthz.Grants{{Verb: jkauthz.Any, Object: jkauthz.Any}}

func figaroVerb(v string) bool {
	return v == jkauthz.Any || slices.Contains(rpc.Verbs(), rpc.Verb(v))
}

func ParseFigaroGrants(texts []string) (jkauthz.Grants, error) {
	schema := Schema(nil)
	gs := make(jkauthz.Grants, 0, len(texts))
	for _, t := range texts {
		g, err := jkauthz.ParseGrant(t)
		if err != nil {
			return nil, err
		}
		if figaroVerb(g.Verb) && g.Object != jkauthz.Any {
			if err := schema.Selectors.Validate(g.Object); err != nil {
				return nil, fmt.Errorf("grant %q: %w", t, err)
			}
		}
		gs = append(gs, g)
	}
	return canonicalGrants(selectors{}, gs), nil
}

func DecodeFigaroGrants(raw json.RawMessage) (jkauthz.Grants, error) {
	var texts []string
	if err := json.Unmarshal(raw, &texts); err != nil || texts == nil {
		return nil, fmt.Errorf("want a JSON array of grants, got %s", raw)
	}
	return ParseFigaroGrants(texts)
}

func verbCovers(a, b string) bool {
	return a == b || (a == jkauthz.Any && figaroVerb(b))
}

func (m selectors) objectCovers(a, b string) bool {
	if a == jkauthz.Any || a == b {
		return true
	}
	root, ok := strings.CutPrefix(a, selLineage)
	if !ok {
		return false
	}
	id, ok := strings.CutPrefix(b, selLineage)
	if !ok {
		id, ok = strings.CutPrefix(b, selAria)
	}
	return ok && (id == root || (m.facts != nil && m.facts.DescendsFrom(id, root)))
}

func (m selectors) grantCovers(a, b jkauthz.Grant) bool {
	return verbCovers(a.Verb, b.Verb) && m.objectCovers(a.Object, b.Object)
}

func (m selectors) covered(gs jkauthz.Grants, g jkauthz.Grant) bool {
	return slices.ContainsFunc(gs, func(h jkauthz.Grant) bool { return m.grantCovers(h, g) })
}

func (m selectors) meet(a, b jkauthz.Grant) (jkauthz.Grant, bool) {
	var g jkauthz.Grant
	switch {
	case verbCovers(a.Verb, b.Verb):
		g.Verb = b.Verb
	case verbCovers(b.Verb, a.Verb):
		g.Verb = a.Verb
	default:
		return g, false
	}
	switch {
	case m.objectCovers(a.Object, b.Object):
		g.Object = b.Object
	case m.objectCovers(b.Object, a.Object):
		g.Object = a.Object
	default:
		return g, false
	}
	return g, true
}

func (m selectors) intersect(a, b jkauthz.Grants) jkauthz.Grants {
	var out jkauthz.Grants
	for _, x := range a {
		for _, y := range b {
			if g, ok := m.meet(x, y); ok {
				out = append(out, g)
			}
		}
	}
	return canonicalGrants(m, out)
}

func canonicalGrants(m selectors, gs jkauthz.Grants) jkauthz.Grants {
	out := make(jkauthz.Grants, 0, len(gs))
	for i, g := range gs {
		dropped := false
		for j, h := range gs {
			if i != j && m.grantCovers(h, g) && (h != g || j < i) {
				dropped = true
				break
			}
		}
		if !dropped {
			out = append(out, g)
		}
	}
	verbs := rpc.Verbs()
	rank := func(v string) int {
		if v == jkauthz.Any {
			return -1
		}
		if i := slices.Index(verbs, rpc.Verb(v)); i >= 0 {
			return i
		}
		return len(verbs)
	}
	slices.SortFunc(out, func(a, b jkauthz.Grant) int {
		if c := rank(a.Verb) - rank(b.Verb); c != 0 {
			return c
		}
		if c := strings.Compare(a.Verb, b.Verb); c != 0 {
			return c
		}
		if (a.Object == jkauthz.Any) != (b.Object == jkauthz.Any) {
			if a.Object == jkauthz.Any {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Object, b.Object)
	})
	return out
}

func resolveSelf(gs jkauthz.Grants, id string) jkauthz.Grants {
	var out jkauthz.Grants
	for i, g := range gs {
		obj := g.Object
		switch obj {
		case selAria + selSelf:
			obj = selAria + id
		case selLineage + selSelf:
			obj = selLineage + id
		default:
			if out != nil {
				out = append(out, g)
			}
			continue
		}
		if out == nil {
			out = append(make(jkauthz.Grants, 0, len(gs)), gs[:i]...)
		}
		out = append(out, jkauthz.Grant{Verb: g.Verb, Object: obj})
	}
	if out == nil {
		return gs
	}
	return out
}

func onlyFigaroVerbs(gs jkauthz.Grants) jkauthz.Grants {
	if !slices.ContainsFunc(gs, func(g jkauthz.Grant) bool { return !figaroVerb(g.Verb) }) {
		return gs
	}
	return slices.DeleteFunc(slices.Clone(gs), func(g jkauthz.Grant) bool { return !figaroVerb(g.Verb) })
}

type parsedGrants struct {
	mu   sync.Mutex
	seen map[string]jkauthz.Grants
}

const parsedGrantsCap = 1024

func (c *parsedGrants) get(raw json.RawMessage) (jkauthz.Grants, error) {
	key := string(raw)
	c.mu.Lock()
	gs, ok := c.seen[key]
	c.mu.Unlock()
	if ok {
		return gs, nil
	}
	gs, err := DecodeFigaroGrants(raw)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.seen == nil || len(c.seen) >= parsedGrantsCap {
		c.seen = map[string]jkauthz.Grants{}
	}
	c.seen[key] = gs
	c.mu.Unlock()
	return gs, nil
}

type FigaroGrants struct {
	Ceiling   jkauthz.Grants
	Own       map[string]jkauthz.Grants
	Effective jkauthz.Grants
	Unread    []string
}

func (p *Policies) figaroFacts() (FigaroFacts, selectors, bool) {
	m, _ := p.schema.Selectors.(selectors)
	ff, ok := m.facts.(FigaroFacts)
	return ff, m, ok
}

func (p *Policies) Effective(figaro string) (jkauthz.Grants, bool) {
	ff, m, ok := p.figaroFacts()
	if !ok || figaro == "" {
		return nil, false
	}
	chain, held := ff.Ancestry(figaro)
	if !held || len(chain) == 0 {
		return nil, false
	}
	var eff jkauthz.Grants
	for i, id := range chain {
		raw, present, err := ff.OwnGrants(id)
		own := everyFigaroVerb
		if err == nil && present {
			own, err = p.parsed.get(raw)
		}
		if err != nil {
			return nil, true
		}
		own = resolveSelf(own, id)
		switch {
		case i > 0:
			if present {
				eff = m.intersect(eff, own)
			} else {
				eff = onlyFigaroVerbs(eff)
			}
		default:
			eff = own
			if ceiling, set := p.ceiling(); set {
				eff = m.intersect(resolveSelf(ceiling, id), own)
			}
		}
	}
	return eff, true
}

func (p *Policies) ExplainGrants(figaro string) (FigaroGrants, bool) {
	ff, _, ok := p.figaroFacts()
	if !ok {
		return FigaroGrants{}, false
	}
	chain, held := ff.Ancestry(figaro)
	if !held || len(chain) == 0 {
		return FigaroGrants{}, false
	}
	ceiling, _ := p.ceiling()
	out := FigaroGrants{Ceiling: ceiling, Own: map[string]jkauthz.Grants{}}
	for _, id := range chain {
		raw, present, err := ff.OwnGrants(id)
		if err == nil && present {
			var gs jkauthz.Grants
			if gs, err = p.parsed.get(raw); err == nil {
				out.Own[id] = gs
			}
		}
		if err != nil {
			out.Unread = append(out.Unread, id)
		}
	}
	out.Effective, _ = p.Effective(figaro)
	return out, true
}

func (p *Policies) ceiling() (jkauthz.Grants, bool) {
	return nil, false
}

func (p *Policies) HoldsGrants(figaro string, want jkauthz.Grants, target string) error {
	held, ok := p.Effective(figaro)
	if !ok {
		return fmt.Errorf("figaro %s is not held here, so it holds no grants to give", figaro)
	}
	_, m, _ := p.figaroFacts()
	for _, g := range resolveSelf(want, target) {
		if !m.covered(held, g) {
			return fmt.Errorf("the writer does not hold %q, and a figaro grants only what it holds", g)
		}
	}
	return nil
}
