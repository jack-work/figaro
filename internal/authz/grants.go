package authz

// GRANTS: the deny-by-default table.
//
// Rules (rules.go) is allow-by-default: a Rule can only DENY, and Check
// returns Allow when nothing objected. That is the right shape for guardrails
// -- "refuse a self-fork mid-turn" -- and exactly the wrong shape for a
// grant table, where the absence of a rule must mean NO.
//
// So Grants is a separate type rather than more Rules, and the difference is
// its zero value: Grants{}.Check denies everything. An empty table, a config
// that failed to parse, a deployment that forgot to say what is allowed --
// all of them refuse, and none of them silently open the daemon.

import (
	"strings"
)

// Grant is one row: these groups may call these methods.
type Grant struct {
	// Groups the caller must hold ONE of. Empty matches no caller: a grant
	// naming no group is a grant to nobody, not a grant to everybody.
	Groups []string
	// Methods this grant permits. "*" is every method; "figaro.*" is an
	// explicit prefix. There is no regex and no substring matching: a glob
	// language is a place for a mistake to hide, and this one has exactly
	// two forms.
	Methods []string
}

// Grants is a deny-by-default policy. Extra runs AFTER a grant matches, so
// the guardrail rules (Rules) still get their veto: a table that permits a
// method does not override an invariant like "no self-fork mid-turn".
type Grants struct {
	Table []Grant
	Extra Policy
}

// Check implements Policy.
func (g Grants) Check(r Request) Decision {
	if !g.permits(r) {
		return Deny("%s: not granted to %s. "+
			"A grant table lists what is allowed; everything else is refused.",
			r.Method, describe(r.Identity))
	}
	if g.Extra != nil {
		return g.Extra.Check(r)
	}
	return Allow()
}

// permits reports whether any row admits this caller for this method.
func (g Grants) permits(r Request) bool {
	for _, grant := range g.Table {
		if grant.admits(r.Identity) && matchAny(grant.Methods, r.Method) {
			return true
		}
	}
	return false
}

// admits reports whether an identity holds one of the grant's groups.
func (g Grant) admits(id Identity) bool {
	for _, want := range g.Groups {
		for _, have := range id.Groups {
			if have == want {
				return true
			}
		}
	}
	return false
}

// matchAny reports whether method matches any pattern. Case-sensitive: method
// names are wire constants, not user input, and a case-insensitive compare
// would admit "FIGARO.QUA" past a table that never mentioned it.
func matchAny(patterns []string, method string) bool {
	for _, p := range patterns {
		if matchMethod(p, method) {
			return true
		}
	}
	return false
}

// matchMethod is the whole glob language: exact, or an explicit "prefix.*".
func matchMethod(pattern, method string) bool {
	if pattern == "*" {
		return true
	}
	if rest, ok := strings.CutSuffix(pattern, ".*"); ok {
		// "figaro.*" matches "figaro.qua" but NOT "figaro": a prefix glob
		// names a namespace, and the namespace itself is not a member.
		return strings.HasPrefix(method, rest+".")
	}
	return pattern == method
}

func describe(id Identity) string {
	if id.Anonymous() && len(id.Groups) == 0 {
		return "an anonymous caller"
	}
	if len(id.Groups) == 0 {
		return id.String() + " (no groups)"
	}
	return id.String() + " in [" + strings.Join(id.Groups, " ") + "]"
}
