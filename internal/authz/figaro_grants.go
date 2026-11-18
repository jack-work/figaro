package authz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
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
		if g.Verb == verbWrite {
			g.Verb = string(rpc.VerbSet)
		}
		sel, glob := splitKeyed(g.Object)
		if strings.Contains(g.Object, keySep) {
			if err := validateKeyGlob(glob); err != nil {
				return nil, fmt.Errorf("grant %q: %w", t, err)
			}
		}
		if figaroVerb(g.Verb) && sel != jkauthz.Any && sel != selParent && sel != selChildren {
			if err := schema.Selectors.Validate(sel); err != nil {
				return nil, fmt.Errorf("grant %q: %w", t, err)
			}
		}
		if g.Verb == string(rpc.VerbSet) && !strings.HasPrefix(t, verbWrite+":") {
			gs = append(gs, jkauthz.Grant{Verb: string(rpc.VerbDelete), Object: g.Object})
		}
		gs = append(gs, g)
	}
	return canonicalGrants(selectors{}, gs), nil
}

const verbWrite = "write"

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
	if a == b {
		return true
	}
	sa, ga := splitKeyed(a)
	sb, gb := splitKeyed(b)
	return globCovers(ga, gb) && m.selectorCovers(sa, sb)
}

func (m selectors) selectorCovers(a, b string) bool {
	if a == jkauthz.Any || a == b {
		return true
	}
	descends := func(id, root string) bool { return m.facts != nil && m.facts.DescendsFrom(id, root) }
	if root, ok := strings.CutPrefix(a, selBelow); ok {
		for _, prefix := range []string{selBelow, selLineage, selAria} {
			if id, ok := strings.CutPrefix(b, prefix); ok {
				return (prefix == selBelow && id == root) || (id != root && descends(id, root))
			}
		}
		return false
	}
	root, ok := strings.CutPrefix(a, selLineage)
	if !ok {
		return false
	}
	id, ok := strings.CutPrefix(b, selLineage)
	if !ok {
		id, ok = strings.CutPrefix(b, selAria)
	}
	if !ok {
		id, ok = strings.CutPrefix(b, selBelow)
	}
	return ok && (id == root || descends(id, root))
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
	sa, ga := splitKeyed(a.Object)
	sb, gb := splitKeyed(b.Object)
	var sel, glob string
	switch {
	case m.selectorCovers(sa, sb):
		sel = sb
	case m.selectorCovers(sb, sa):
		sel = sa
	default:
		return g, false
	}
	switch {
	case globCovers(ga, gb):
		glob = gb
	case globCovers(gb, ga):
		glob = ga
	default:
		return g, false
	}
	g.Object = keyed(sel, glob)
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

func resolveSelf(gs jkauthz.Grants, id, parent string) jkauthz.Grants {
	var out jkauthz.Grants
	for i, g := range gs {
		sel, glob := splitKeyed(g.Object)
		switch sel {
		case selAria + selSelf:
			sel = selAria + id
		case selLineage + selSelf:
			sel = selLineage + id
		case selParent:
			sel = selAria + parent
		case selChildren:
			sel = selBelow + id
		default:
			if out != nil {
				out = append(out, g)
			}
			continue
		}
		if out == nil {
			out = append(make(jkauthz.Grants, 0, len(gs)), gs[:i]...)
		}
		if sel == selAria {
			continue
		}
		out = append(out, jkauthz.Grant{Verb: g.Verb, Object: keyed(sel, glob)})
	}
	if out == nil {
		return gs
	}
	return out
}

func unresolveSelf(gs jkauthz.Grants, id string) jkauthz.Grants {
	out := slices.Clone(gs)
	for i, g := range out {
		sel, glob := splitKeyed(g.Object)
		switch sel {
		case selAria + id:
			out[i].Object = keyed(selAria+selSelf, glob)
		case selLineage + id:
			out[i].Object = keyed(selLineage+selSelf, glob)
		case selBelow + id:
			out[i].Object = keyed(selChildren, glob)
		}
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

func (p *Policies) Effective(figaro string) (jkauthz.Grants, bool, error) {
	ff, _, ok := p.figaroFacts()
	if !ok || figaro == "" {
		return nil, false, nil
	}
	chain, held := ff.Ancestry(figaro)
	if !held || len(chain) == 0 {
		return nil, false, nil
	}
	eff, err := p.effectiveDown(chain)
	return eff, true, err
}

func (p *Policies) EffectiveAlone(figaro string) (jkauthz.Grants, error) {
	ff, _, ok := p.figaroFacts()
	if !ok || figaro == "" {
		return nil, fmt.Errorf("%s%s is not held here", figaroPrefix, figaro)
	}
	if _, held := ff.Ancestry(figaro); !held {
		return nil, fmt.Errorf("%s%s is not held here", figaroPrefix, figaro)
	}
	return p.effectiveDown([]string{figaro})
}

func (p *Policies) SuccessorScopes(figaro string) (scopes jkauthz.Grants, narrowed, held bool, err error) {
	eff, held, err := p.Effective(figaro)
	if !held || err != nil {
		return nil, false, held, err
	}
	alone, err := p.EffectiveAlone(figaro)
	if err != nil {
		return nil, false, true, err
	}
	if p.Covers(eff, alone) {
		return nil, false, true, nil
	}
	return unresolveSelf(eff, figaro), true, true, nil
}

func (p *Policies) effectiveDown(chain []string) (jkauthz.Grants, error) {
	ff, m, _ := p.figaroFacts()
	var eff jkauthz.Grants
	for i, id := range chain {
		raw, present, err := ff.OwnGrants(id)
		own := everyFigaroVerb
		if err == nil && present {
			own, err = p.parsed.get(raw)
		}
		if err != nil {
			return nil, fmt.Errorf("the scopes of %s cannot be read: %w", id, err)
		}
		parent := ""
		if i > 0 {
			parent = chain[i-1]
		}
		own = resolveSelf(own, id, parent)
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
				eff = m.intersect(resolveSelf(ceiling, id, ""), own)
			}
		}
	}
	return eff, nil
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
	out.Effective, _, _ = p.Effective(figaro)
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
		return fmt.Errorf("[[fence]] is gone: a figaro's own system.scopes narrows it, and [figaros] permissions narrows every top-level one (figaro scopes <id> shows the result)")
	}
	return nil
}

type ceilingTable struct {
	set     *jkauthz.Grants
	retired []string
}

func splitCeiling(policy []byte) ([]byte, ceilingTable, error) {
	var doc map[string]any
	md, err := toml.Decode(string(policy), &doc)
	if err != nil {
		return nil, ceilingTable{}, err
	}
	if !md.IsDefined("figaros") {
		return policy, ceilingTable{}, nil
	}
	var typed struct {
		Figaros struct {
			Permissions []string `toml:"permissions"`
			Grants      []string `toml:"grants"`
		} `toml:"figaros"`
	}
	md, err = toml.Decode(string(policy), &typed)
	if err != nil {
		return nil, ceilingTable{}, err
	}
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "figaros" {
			return nil, ceilingTable{}, fmt.Errorf("unknown key %s: [figaros] holds only permissions", k)
		}
	}
	var out ceilingTable
	texts := typed.Figaros.Permissions
	switch newName, oldName := md.IsDefined("figaros", "permissions"), md.IsDefined("figaros", "grants"); {
	case newName && oldName:
		return nil, ceilingTable{}, fmt.Errorf("[figaros] sets both permissions and grants: grants is the old name for permissions; keep one")
	case oldName:
		texts = typed.Figaros.Grants
		out.retired = []string{"[figaros] grants"}
	}
	if texts == nil {
		return nil, ceilingTable{}, fmt.Errorf("[figaros] needs permissions: the most any figaro may do here; [] is nothing")
	}
	ceiling, err := ParseFigaroGrants(texts)
	if err != nil {
		return nil, ceilingTable{}, fmt.Errorf("[figaros]: %w", err)
	}
	out.set = &ceiling
	rest := cutTable(policy, ceilingHeader)
	var left map[string]any
	delete(doc, "figaros")
	if _, err := toml.Decode(string(rest), &left); err != nil || !reflect.DeepEqual(doc, left) {
		return nil, ceilingTable{}, fmt.Errorf("the [figaros] table could not be set apart from the roles: write it as a [figaros] table of its own")
	}
	return rest, out, nil
}

func warnRetiredNames(what, where string, names []string) {
	for _, name := range names {
		renamed := strings.Replace(name, " grants", " permissions", 1)
		slog.Warn(what+" says "+name+", the old name; write "+renamed, "in", where)
	}
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

func (p *Policies) SetBoxesRequiringClaim(boxes []string) {
	boxes = slices.Clone(boxes)
	p.claimRequired.Store(&boxes)
}

func (p *Policies) claimRequiredIn(box string) bool {
	boxes := p.claimRequired.Load()
	return boxes != nil && slices.Contains(*boxes, box)
}

type FigaroView struct {
	p      *Policies
	figaro string
	eff    jkauthz.Grants
	held   bool
	err    error
}

func (p *Policies) Figaro(figaro string) FigaroView {
	v := FigaroView{p: p, figaro: figaro}
	if figaro != "" {
		v.eff, v.held, v.err = p.Effective(figaro)
		if v.err != nil {
			slog.Warn("authz: a figaro's scopes cannot be read, so it holds none", "figaro", figaro, "err", v.err)
		}
	}
	return v
}

func (v FigaroView) Allow(s jkauthz.Subject, verb rpc.Verb, r Resource) Verdict {
	d := v.p.Allow(s, verb, r)
	if !d.Allowed {
		return d
	}
	if why := v.refuses(s, verb, r); why != "" {
		return Verdict{narrowed: why, unreadable: v.err != nil}
	}
	return d
}

func (v FigaroView) refuses(s jkauthz.Subject, verb rpc.Verb, r Resource) string {
	if v.figaro == "" {
		return ""
	}
	if !v.held {
		return fmt.Sprintf("%s%s is not held here", figaroPrefix, v.figaro)
	}
	if v.err != nil {
		return fmt.Sprintf("%s%s holds no scopes: %v", figaroPrefix, v.figaro, v.err)
	}
	m, _ := v.p.schema.Selectors.(selectors)
	for _, g := range v.eff {
		sel, glob := splitKeyed(g.Object)
		if glob != "" && verb != rpc.VerbSet && verb != rpc.VerbDelete {
			continue
		}
		if verbCovers(g.Verb, string(verb)) && v.reaches(m, sel, s, verb, r) {
			return ""
		}
	}
	if len(v.eff) == 0 {
		return fmt.Sprintf("%s%s may not %s on %s: it holds no scopes", figaroPrefix, v.figaro, verb, r)
	}
	return fmt.Sprintf("%s%s may not %s on %s: its scopes (%s) do not reach it", figaroPrefix, v.figaro, verb, r, v.eff)
}

func (v FigaroView) reaches(m selectors, sel string, s jkauthz.Subject, verb rpc.Verb, r Resource) bool {
	if sel == jkauthz.Any || m.Match(sel, s, r) {
		return true
	}
	if verb != rpc.VerbSend || r.Kind != ResourceAria || !strings.HasPrefix(r.ID, "@") || m.facts == nil {
		return false
	}
	target := m.facts.RoleTarget(r.ID)
	return target != "" && m.Match(sel, s, Aria(target))
}

func (v FigaroView) AllowsKey(s jkauthz.Subject, verb rpc.Verb, r Resource, key string) bool {
	if v.figaro == "" {
		return true
	}
	if !v.held || v.err != nil {
		return false
	}
	m, _ := v.p.schema.Selectors.(selectors)
	for _, g := range v.eff {
		sel, glob := splitKeyed(g.Object)
		if verbCovers(g.Verb, string(verb)) && (glob == "" || KeyGlobMatch(glob, key)) && v.reaches(m, sel, s, verb, r) {
			return true
		}
	}
	return false
}

func (v FigaroView) AllowWhole(s jkauthz.Subject, verb rpc.Verb, r Resource) bool {
	all, _ := v.KeyGlobs(s, verb, r)
	return all
}

func (v FigaroView) KeyGlobs(s jkauthz.Subject, verb rpc.Verb, r Resource) (all bool, globs []string) {
	if v.figaro == "" {
		return true, nil
	}
	if !v.held || v.err != nil {
		return false, nil
	}
	m, _ := v.p.schema.Selectors.(selectors)
	for _, g := range v.eff {
		sel, glob := splitKeyed(g.Object)
		if !verbCovers(g.Verb, string(verb)) || !v.reaches(m, sel, s, verb, r) {
			continue
		}
		if glob == "" {
			return true, nil
		}
		globs = append(globs, glob)
	}
	return false, globs
}

func (p *Policies) HoldsGrants(figaro string, want jkauthz.Grants, target, parent string) error {
	held, ok, err := p.Effective(figaro)
	if !ok {
		return fmt.Errorf("figaro %s is not held here, so it holds no scopes to give", figaro)
	}
	if err != nil {
		return fmt.Errorf("figaro %s gives nothing: %w", figaro, err)
	}
	ff, m, _ := p.figaroFacts()
	if chain, ok := ff.Ancestry(target); parent == "" && ok && len(chain) > 1 {
		parent = chain[len(chain)-2]
	}
	for _, g := range resolveSelf(want, target, parent) {
		if !m.covered(held, g) {
			return fmt.Errorf("the writer does not hold %q, and a figaro gives only what it holds", g)
		}
	}
	return nil
}

func (p *Policies) TopologyReach(s jkauthz.Subject, figaro string, verb rpc.Verb, target string) error {
	return p.Shadow(LineageReach, p.lineageReach(s, figaro, verb, target))
}

func (p *Policies) lineageReach(s jkauthz.Subject, figaro string, verb rpc.Verb, target string) error {
	ff, m, ok := p.figaroFacts()
	if !ok || figaro == "" || target == figaro || m.facts.DescendsFrom(target, figaro) {
		return nil
	}
	if p.writtenScopes(ff, figaro) && p.Figaro(figaro).refuses(s, verb, Aria(target)) == "" {
		return nil
	}
	return fmt.Errorf("%s%s may not %s %s: a figaro's demote, promote, retire and kill reach its own lineage, and reach beyond it is a grant written in system.scopes (%s:*)", figaroPrefix, figaro, verb, target, verb)
}

func (p *Policies) writtenScopes(ff FigaroFacts, figaro string) bool {
	if _, set := p.ceiling(); set {
		return true
	}
	chain, _ := ff.Ancestry(figaro)
	for _, id := range chain {
		if _, present, err := ff.OwnGrants(id); err == nil && present {
			return true
		}
	}
	return false
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
