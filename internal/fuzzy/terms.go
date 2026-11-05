package fuzzy

import (
	"strings"
	"unicode/utf8"
)

type termKind uint8

const (
	termFuzzy termKind = iota
	termExact
	termPrefix
	termSuffix
)

type Term struct {
	kind    termKind
	negate  bool
	onKey   bool
	onValue bool
	text    string
	lower   string
	runes   []rune
}

func Parse(query string) []Term {
	var out []Term
	for _, f := range strings.Fields(query) {
		t := Term{onKey: true, onValue: true}
		switch {
		case strings.HasPrefix(f, "k:"):
			t.onValue, f = false, f[2:]
		case strings.HasPrefix(f, "v:"):
			t.onKey, f = false, f[2:]
		}
		if strings.HasPrefix(f, "!") {
			t.negate, f = true, f[1:]
		}
		switch {
		case strings.HasPrefix(f, "'"):
			t.kind, f = termExact, f[1:]
		case strings.HasPrefix(f, "^"):
			t.kind, f = termPrefix, f[1:]
		case len(f) > 1 && strings.HasSuffix(f, "$"):
			t.kind, f = termSuffix, f[:len(f)-1]
		}
		if t.negate && t.kind == termFuzzy {
			t.kind = termExact
		}
		if f == "" {
			continue
		}
		t.text, t.lower, t.runes = f, strings.ToLower(f), []rune(f)
		out = append(out, t)
	}
	return out
}

func (t Term) matchKey(m *Matcher, key *Text, plain string) (score int, ok bool) {
	if !t.onKey {
		return 0, false
	}
	if t.kind == termFuzzy {
		got, found := m.Match(key, t.runes)
		return got.Score, found
	}
	low := strings.ToLower(plain)
	switch t.kind {
	case termExact:
		ok = strings.Contains(low, t.lower)
	case termPrefix:
		ok = strings.HasPrefix(low, t.lower)
	case termSuffix:
		ok = strings.HasSuffix(low, t.lower)
	}
	if ok {
		score = 16 * utf8.RuneCountInString(t.text)
	}
	return score, ok
}

func (t Term) valueAt(lowerValue string) int {
	if !t.onValue {
		return -1
	}
	switch t.kind {
	case termPrefix:
		if strings.HasPrefix(lowerValue, t.lower) {
			return 0
		}
		return -1
	case termSuffix:
		if strings.HasSuffix(lowerValue, t.lower) {
			return len(lowerValue) - len(t.lower)
		}
		return -1
	}
	return strings.Index(lowerValue, t.lower)
}

func (t Term) keyPositions(m *Matcher, key *Text, plain string) []int {
	if !t.onKey || t.negate {
		return nil
	}
	if t.kind == termFuzzy {
		return m.Positions(key, t.runes)
	}
	low := strings.ToLower(plain)
	at := -1
	switch t.kind {
	case termExact:
		at = strings.Index(low, t.lower)
	case termPrefix:
		if strings.HasPrefix(low, t.lower) {
			at = 0
		}
	case termSuffix:
		if strings.HasSuffix(low, t.lower) {
			at = len(low) - len(t.lower)
		}
	}
	if at < 0 {
		return nil
	}
	first := utf8.RuneCountInString(plain[:at])
	out := make([]int, 0, len(t.runes))
	for i := range t.runes {
		out = append(out, first+i)
	}
	return out
}

func (t Term) ValueText() (string, bool) { return t.lower, t.onValue && !t.negate }
