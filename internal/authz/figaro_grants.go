package authz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

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
	Chain      []string
	Ceiling    jkauthz.Grants
	CeilingSet bool
	Own        map[string]jkauthz.Grants
	Effective  jkauthz.Grants
	Unread     []string
}

func (p *Policies) figaroFacts() (FigaroFacts, selectors, bool) {
	m, _ := p.schema.Selectors.(selectors)
	ff, ok := m.facts.(FigaroFacts)
	return ff, m, ok
}

func (p *Policies) Effective(figaro string) (jkauthz.Grants, bool) {
	ff, _, ok := p.figaroFacts()
	if !ok || figaro == "" {
		return nil, false
	}
	chain, held := ff.Ancestry(figaro)
	if !held || len(chain) == 0 {
		return nil, false
	}
	return p.effectiveDown(chain), true
}

func (p *Policies) EffectiveAlone(figaro string) (jkauthz.Grants, bool) {
	ff, _, ok := p.figaroFacts()
	if !ok || figaro == "" {
		return nil, false
	}
	if _, held := ff.Ancestry(figaro); !held {
		return nil, false
	}
	return p.effectiveDown([]string{figaro}), true
}

func (p *Policies) effectiveDown(chain []string) jkauthz.Grants {
	ff, m, _ := p.figaroFacts()
	var eff jkauthz.Grants
	for i, id := range chain {
		raw, present, err := ff.OwnGrants(id)
		own := everyFigaroVerb
		if err == nil && present {
			own, err = p.parsed.get(raw)
		}
		if err != nil {
			return nil
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
	return eff
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
	ceiling, set := p.ceiling()
	out := FigaroGrants{Chain: chain, Ceiling: ceiling, CeilingSet: set, Own: map[string]jkauthz.Grants{}}
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
	c := p.figaroCeiling.Load()
	if c == nil {
		return nil, false
	}
	return *c, true
}

func refuseFences(policy []byte) error {
	var doc map[string]any
	if _, err := toml.Decode(string(policy), &doc); err != nil {
		return err
	}
	if _, ok := doc["fence"]; ok {
		return fmt.Errorf("[[fence]] is gone: a figaro's own system.grants narrows it, and [figaros] grants narrows every top-level one (figaro grants <id> shows the result)")
	}
	return nil
}

func splitCeiling(policy []byte) ([]byte, *jkauthz.Grants, error) {
	var doc map[string]any
	md, err := toml.Decode(string(policy), &doc)
	if err != nil {
		return nil, nil, err
	}
	if !md.IsDefined("figaros") {
		return policy, nil, nil
	}
	var typed struct {
		Figaros struct {
			Grants []string `toml:"grants"`
		} `toml:"figaros"`
	}
	md, err = toml.Decode(string(policy), &typed)
	if err != nil {
		return nil, nil, err
	}
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "figaros" {
			return nil, nil, fmt.Errorf("unknown key %s: [figaros] holds only grants", k)
		}
	}
	if typed.Figaros.Grants == nil {
		return nil, nil, fmt.Errorf("[figaros] needs grants: the most any figaro may do here; [] is nothing")
	}
	ceiling, err := ParseFigaroGrants(typed.Figaros.Grants)
	if err != nil {
		return nil, nil, fmt.Errorf("[figaros]: %w", err)
	}
	grants := cutTable(policy, ceilingHeader)
	var rest map[string]any
	delete(doc, "figaros")
	if _, err := toml.Decode(string(grants), &rest); err != nil || !reflect.DeepEqual(doc, rest) {
		return nil, nil, fmt.Errorf("the [figaros] table could not be set apart from the grants: write it as a [figaros] table of its own")
	}
	return grants, &ceiling, nil
}

var (
	ceilingHeader  = regexp.MustCompile(`^\s*\[\s*figaros\s*\]\s*(#.*)?$`)
	anyTableHeader = regexp.MustCompile(`^\s*\[`)
)

func cutTable(data []byte, header *regexp.Regexp) []byte {
	out := make([]byte, 0, len(data))
	cutting := false
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		bare := bytes.TrimRight(line, "\r\n")
		switch {
		case header.Match(bare):
			cutting = true
			continue
		case cutting && anyTableHeader.Match(bare):
			cutting = false
		}
		if !cutting {
			out = append(out, line...)
		}
	}
	return out
}

func (p *Policies) SetFigaroClaimRequired(on bool) { p.claimRequired.Store(on) }

func (p *Policies) FigaroClaimRequired() bool { return p.claimRequired.Load() }

type FigaroView struct {
	p      *Policies
	figaro string
	eff    jkauthz.Grants
	held   bool
}

func (p *Policies) Figaro(figaro string) FigaroView {
	v := FigaroView{p: p, figaro: figaro}
	if figaro != "" {
		v.eff, v.held = p.Effective(figaro)
	}
	return v
}

func (v FigaroView) Allow(s jkauthz.Subject, verb rpc.Verb, r Resource) Verdict {
	d := v.p.Allow(s, verb, r)
	if !d.Allowed {
		return d
	}
	if why := v.refuses(s, verb, r); why != "" {
		return Verdict{narrowed: why}
	}
	return d
}

func (v FigaroView) refuses(s jkauthz.Subject, verb rpc.Verb, r Resource) string {
	if !v.held {
		return ""
	}
	m, _ := v.p.schema.Selectors.(selectors)
	for _, g := range v.eff {
		if verbCovers(g.Verb, string(verb)) && (g.Object == jkauthz.Any || m.Match(g.Object, s, r)) {
			return ""
		}
	}
	if len(v.eff) == 0 {
		return fmt.Sprintf("%s%s may not %s on %s: it holds no grants", figaroPrefix, v.figaro, verb, r)
	}
	return fmt.Sprintf("%s%s may not %s on %s: its grants (%s) do not reach it", figaroPrefix, v.figaro, verb, r, v.eff)
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

func (p *Policies) Covers(held, want jkauthz.Grants) bool {
	m, _ := p.schema.Selectors.(selectors)
	for _, g := range want {
		if !m.covered(held, g) {
			return false
		}
	}
	return true
}

const figaroPrefix = "figaro:"

func (p *Policies) AllowFigaro(s jkauthz.Subject, figaro string, verb rpc.Verb, r Resource) Verdict {
	d := p.Allow(s, verb, r)
	if !d.Allowed || figaro == "" {
		return d
	}
	return p.Figaro(figaro).Allow(s, verb, r)
}

// ExportGrants spells a set for anyone outside figaro: a * verb means
// figaro's verbs only, which a reader that treats * as every verb would
// take for egress and credentials too, so it is written out verb by verb.
func ExportGrants(gs jkauthz.Grants) []string {
	out := make(jkauthz.Grants, 0, len(gs))
	for _, g := range gs {
		if g.Verb != jkauthz.Any {
			out = append(out, g)
			continue
		}
		for _, v := range rpc.Verbs() {
			out = append(out, jkauthz.Grant{Verb: string(v), Object: g.Object})
		}
	}
	strs := canonicalGrants(selectors{}, out).Strings()
	if strs == nil {
		return []string{}
	}
	return strs
}
