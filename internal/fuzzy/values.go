package fuzzy

import "encoding/json"

func MatchValues(m *Matcher, terms []Term, values ...string) bool {
	entries := make([]*Entry, 0, len(values))
	for _, v := range values {
		raw, _ := json.Marshal(v)
		entries = append(entries, newEntry("", nil, raw))
	}
	positive := false
	for _, t := range terms {
		t.onKey, t.onValue = false, true
		hit := false
		for _, e := range entries {
			if t.matchValue(m, e).ok {
				hit = true
				break
			}
		}
		if t.negate {
			if hit {
				return false
			}
			continue
		}
		if !hit {
			return false
		}
		positive = true
	}
	return positive
}
