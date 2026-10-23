package cli

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/render"
)

type searchPattern struct {
	query string
	fold  bool
	re    *regexp.Regexp
}

var (
	patternMu   sync.Mutex
	lastPattern *searchPattern
)

func patternOf(q string) *searchPattern {
	patternMu.Lock()
	defer patternMu.Unlock()
	if lastPattern != nil && lastPattern.query == q {
		return lastPattern
	}
	lastPattern = compileSearch(q)
	return lastPattern
}

func compileSearch(q string) *searchPattern {
	p := &searchPattern{query: q, fold: !hasUpper(q)}
	literal := regexp.QuoteMeta(q) == q
	if literal && (!p.fold || isASCII(q)) {
		return p
	}
	expr := q
	if _, err := regexp.Compile(expr); err != nil {
		expr = regexp.QuoteMeta(q)
	}
	if p.fold {
		expr = "(?i)" + expr
	}
	p.re = regexp.MustCompile(expr)
	return p
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (p *searchPattern) literal() bool { return p.re == nil }

func (p *searchPattern) matchesRow(row string) bool {
	if p.query == "" {
		return false
	}
	if p.literal() {
		return visibleIndexFold(row, p.query, p.fold) >= 0
	}
	_, _, ok := p.next(visibleOf(row), 0)
	return ok
}

func visibleOf(row string) string {
	if !strings.ContainsRune(row, '\x1b') {
		return row
	}
	var b strings.Builder
	b.Grow(len(row))
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			i = render.SkipEscape(row, i)
			continue
		}
		b.WriteByte(row[i])
		i++
	}
	return b.String()
}

func (p *searchPattern) next(s string, from int) (start, end int, ok bool) {
	for from <= len(s) {
		if p.literal() {
			i := indexFold(s[from:], p.query, p.fold)
			if i < 0 {
				return 0, 0, false
			}
			return from + i, from + i + len(p.query), true
		}
		loc := p.re.FindStringIndex(s[from:])
		if loc == nil {
			return 0, 0, false
		}
		if loc[1] > loc[0] {
			return from + loc[0], from + loc[1], true
		}
		_, size := utf8.DecodeRuneInString(s[from+loc[0]:])
		from += loc[0] + max(size, 1)
	}
	return 0, 0, false
}

func (p *searchPattern) spans(s string) [][2]int {
	var out [][2]int
	for from := 0; ; {
		start, end, ok := p.next(s, from)
		if !ok {
			return out
		}
		out = append(out, [2]int{start, end})
		from = end
	}
}

func (p *searchPattern) inText(s string) bool {
	if p.literal() {
		return indexFold(s, p.query, p.fold) >= 0
	}
	_, _, ok := p.next(s, 0)
	return ok
}

func indexFold(s, q string, fold bool) int {
	if !fold {
		return strings.Index(s, q)
	}
	for i := 0; i+len(q) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(q)], q) {
			return i
		}
	}
	return -1
}

func equalFoldASCII(a, b string) bool {
	for i := 0; i < len(a); i++ {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func visibleIndexFold(row, q string, fold bool) int {
	if q == "" {
		return 0
	}
	for start := 0; start < len(row); {
		if row[start] == '\x1b' {
			start = render.SkipEscape(row, start)
			continue
		}
		i, j := start, 0
		for j < len(q) && i < len(row) {
			if row[i] == '\x1b' {
				i = render.SkipEscape(row, i)
				continue
			}
			if row[i] != q[j] && (!fold || lowerASCII(row[i]) != lowerASCII(q[j])) {
				break
			}
			i++
			j++
		}
		if j == len(q) {
			return start
		}
		start++
	}
	return -1
}
