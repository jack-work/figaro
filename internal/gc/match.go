package gc

import "github.com/jack-work/figaro/internal/fuzzy"

type matcher struct {
	m     *fuzzy.Matcher
	terms []fuzzy.Term
}

func newMatcher(query string) *matcher {
	if query == "" {
		return nil
	}
	return &matcher{m: fuzzy.NewMatcher(), terms: fuzzy.Parse(query)}
}

func (m *matcher) matches(values ...string) bool {
	return fuzzy.MatchValues(m.m, m.terms, values...)
}
