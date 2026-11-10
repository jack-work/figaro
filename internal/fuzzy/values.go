package fuzzy

import "encoding/json"

func MatchValues(m *Matcher, terms []Term, values ...string) bool {
	ok, spread := matchValues(m, terms, values)
	return ok && !spread
}

func matchValues(m *Matcher, terms []Term, values []string) (ok, spread bool) {
	entries := make([]*Entry, 0, len(values))
	for _, v := range values {
		raw, _ := json.Marshal(v)
		entries = append(entries, newEntry("", nil, raw))
	}
	positive := false
	for _, t := range terms {
		t.onKey, t.onValue = false, true
		hit, tight := false, false
		for _, e := range entries {
			if v := t.matchValue(m, e); v.ok {
				hit = true
				if !v.spread {
					tight = true
					break
				}
			}
		}
		if t.negate {
			if hit {
				return false, false
			}
			continue
		}
		if !hit {
			return false, false
		}
		spread = spread || !tight
		positive = true
	}
	return positive, spread
}
