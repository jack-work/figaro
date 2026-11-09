package authz

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

const (
	figaroPrefix = "figaro:"
	figaroAny    = figaroPrefix + jkauthz.Any
	figaroSelf   = figaroPrefix + selSelf
	figaroRole   = figaroPrefix + "@"
)

type fence struct {
	n     int
	verbs []rpc.Verb
	on    []string
	allow []string
	deny  []string
}

type fenceDoc struct {
	Verbs []string `toml:"verbs"`
	On    []string `toml:"on"`
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
}

func splitFences(data []byte, schema jkauthz.Schema[Resource]) ([]byte, []fence, error) {
	var typed struct {
		Fence []fenceDoc `toml:"fence"`
	}
	md, err := toml.Decode(string(data), &typed)
	if err != nil {
		return nil, nil, err
	}
	if !md.IsDefined("fence") {
		return data, nil, nil
	}
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "fence" {
			return nil, nil, fmt.Errorf("unknown key %s", k)
		}
	}
	fences := make([]fence, 0, len(typed.Fence))
	for i, d := range typed.Fence {
		f, err := compileFence(i+1, d, schema)
		if err != nil {
			return nil, nil, err
		}
		fences = append(fences, f)
	}
	grants := cutFenceTables(data)
	if err := sameGrants(data, grants); err != nil {
		return nil, nil, err
	}
	return grants, fences, nil
}

var (
	fenceHeader = regexp.MustCompile(`^\s*\[\[\s*fence\s*\]\]\s*(#.*)?$`)
	tableHeader = regexp.MustCompile(`^\s*\[`)
)

func cutFenceTables(data []byte) []byte {
	out := make([]byte, 0, len(data))
	cutting := false
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		bare := bytes.TrimRight(line, "\r\n")
		switch {
		case fenceHeader.Match(bare):
			cutting = true
			continue
		case cutting && tableHeader.Match(bare):
			cutting = false
		}
		if !cutting {
			out = append(out, line...)
		}
	}
	return out
}

func sameGrants(whole, grants []byte) error {
	var want, got map[string]any
	if _, err := toml.Decode(string(whole), &want); err != nil {
		return err
	}
	delete(want, "fence")
	if _, err := toml.Decode(string(grants), &got); err != nil || !reflect.DeepEqual(want, got) {
		return fmt.Errorf("the [[fence]] tables could not be set apart from the grants: write each fence as a [[fence]] table of its own")
	}
	return nil
}

func compileFence(n int, d fenceDoc, schema jkauthz.Schema[Resource]) (fence, error) {
	f := fence{n: n, on: d.On, allow: d.Allow, deny: d.Deny}
	if len(d.Verbs) == 0 || len(d.On) == 0 {
		return f, fmt.Errorf("fence %d: needs verbs and on", n)
	}
	if (len(d.Allow) == 0) == (len(d.Deny) == 0) {
		return f, fmt.Errorf("fence %d: needs allow or deny, not both", n)
	}
	for _, v := range d.Verbs {
		if v == jkauthz.Any {
			f.verbs = rpc.Verbs()
			break
		}
		if !slices.Contains(schema.Verbs, v) {
			return f, fmt.Errorf("fence %d: unknown verb %q", n, v)
		}
		f.verbs = append(f.verbs, rpc.Verb(v))
	}
	for _, sel := range d.On {
		if sel == jkauthz.Any {
			continue
		}
		if err := schema.Selectors.Validate(sel); err != nil {
			return f, fmt.Errorf("fence %d: selector %q: %w", n, sel, err)
		}
	}
	for _, name := range slices.Concat(d.Allow, d.Deny) {
		if err := validFigaroName(name); err != nil {
			return f, fmt.Errorf("fence %d: %w", n, err)
		}
	}
	return f, nil
}

func validFigaroName(name string) error {
	rest, ok := strings.CutPrefix(name, figaroPrefix)
	if !ok || rest == "" || rest == "@" {
		return fmt.Errorf("%q: want figaro:<id>, figaro:@<role>, figaro:self or figaro:*", name)
	}
	if rest == jkauthz.Any || rest == selSelf {
		return nil
	}
	if err := rpc.ValidateAriaID(rest); err != nil {
		return fmt.Errorf("%q: %w", name, err)
	}
	return nil
}

func (f fence) covers(m selectors, s jkauthz.Subject, verb rpc.Verb, r Resource) bool {
	if !slices.Contains(f.verbs, verb) {
		return false
	}
	for _, sel := range f.on {
		if sel == jkauthz.Any || m.Match(sel, s, r) {
			return true
		}
	}
	return false
}

func (f fence) names(m selectors, figaro string, r Resource, list []string) bool {
	for _, name := range list {
		switch {
		case name == figaroAny:
			return true
		case name == figaroSelf:
			if r.Kind == ResourceAria && r.ID == figaro {
				return true
			}
		case strings.HasPrefix(name, figaroRole):
			if m.facts != nil && m.facts.RoleTarget(name[len(figaroPrefix):]) == figaro {
				return true
			}
		case name[len(figaroPrefix):] == figaro:
			return true
		}
	}
	return false
}

func (f fence) refuses(m selectors, figaro string, r Resource) string {
	if len(f.allow) > 0 && !f.names(m, figaro, r, f.allow) {
		return fmt.Sprintf("fence %d lets only %s", f.n, strings.Join(f.allow, ", "))
	}
	if len(f.deny) > 0 && f.names(m, figaro, r, f.deny) {
		return fmt.Sprintf("fence %d refuses %s", f.n, strings.Join(f.deny, ", "))
	}
	return ""
}

func (p *Policies) fenced(s jkauthz.Subject, figaro string, verb rpc.Verb, r Resource) string {
	if figaro == "" || s.Principal != p.self {
		return ""
	}
	fences := p.fences.Load()
	if fences == nil {
		return ""
	}
	m, _ := p.schema.Selectors.(selectors)
	for _, f := range *fences {
		if !f.covers(m, s, verb, r) {
			continue
		}
		if why := f.refuses(m, figaro, r); why != "" {
			return fmt.Sprintf("%s%s may not %s on %s: %s", figaroPrefix, figaro, verb, r, why)
		}
	}
	return ""
}

func (p *Policies) AllowFigaro(s jkauthz.Subject, figaro string, verb rpc.Verb, r Resource) Verdict {
	v := p.Allow(s, verb, r)
	if !v.Allowed {
		return v
	}
	if why := p.fenced(s, figaro, verb, r); why != "" {
		return Verdict{fence: why}
	}
	return v
}
