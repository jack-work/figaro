package fuzzy

import (
	"sync"

	"github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
)

var initOnce sync.Once

type Text struct {
	chars util.Chars
}

func NewText(s string) Text {
	return Text{chars: util.ToChars([]byte(s))}
}

type Matcher struct {
	slab *util.Slab
}

func NewMatcher() *Matcher {
	initOnce.Do(func() { algo.Init("default") })
	return &Matcher{slab: util.MakeSlab(100*1024, 2048)}
}

type Match struct {
	Score     int
	Positions []int
}

func (m *Matcher) Match(t *Text, pattern []rune) (Match, bool) {
	return m.match(t, pattern, false)
}

func (m *Matcher) Positions(t *Text, pattern []rune) []int {
	got, _ := m.match(t, pattern, true)
	return got.Positions
}

func (m *Matcher) match(t *Text, pattern []rune, withPos bool) (Match, bool) {
	if len(pattern) == 0 {
		return Match{}, true
	}
	r, pos := algo.FuzzyMatchV2(false, true, true, &t.chars, pattern, withPos, m.slab)
	if r.Start < 0 {
		return Match{}, false
	}
	var positions []int
	if pos != nil {
		positions = append(positions, (*pos)...)
	}
	return Match{Score: r.Score, Positions: positions}, true
}
