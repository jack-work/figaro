package cli

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
)

const formDeltaKeyCap = 28

// formDeltaValueFloor is the narrowest a value is squeezed to before the row
// gives up on showing both sides.
const formDeltaValueFloor = 6

// U+2442 is carried by FreeMono, the same fallback that paints the pit's glyphs.
const forkGlyph = "⑂"

const deltaGlyph = "Δ"

const deadGlyph = "⊘"

// absentValue is the missing side of a transition: a key born has no old, a key
// removed has no new.
const absentValue = "∅"

const emptyValue = `""`

type deltaRow struct {
	glyph  string
	key    string
	from   deltaCell
	to     deltaCell
	banner bool
}

// Absence and the empty string are different rows.
type deltaCell struct {
	text    string
	present bool
}

// deltaRows: the fork banner first when lifted, then this figaro's own board,
// then every other form by id. liftFork is the inquiry's: a fork happens only
// on the turn's question.
func deltaRows(deltas map[string]livedoc.FormDelta, liftFork bool) []deltaRow {
	if len(deltas) == 0 {
		return nil
	}
	groups, order := groupDeltas(deltas)
	var out []deltaRow
	for _, formID := range order {
		g := groups[formID]
		prefix := deltaFormName(formID, g)
		if g.deleted {
			out = append(out, deltaRow{glyph: deadGlyph, key: sanitize(formID), banner: true})
		}
		for _, k := range g.keys {
			// A lifted fork leaves no rows behind: the glyph and
			// parent id ride the question's header.
			if liftFork && forkKey(g.kind, k) && forkParent(g) != "" {
				continue
			}
			d := g.byKey[k]
			row := deltaRow{key: sanitize(prefix + k), from: unquote(d.Prev)}
			if d.Event != livedoc.FormRemoved {
				row.to = unquote(d.Value)
			}
			out = append(out, row)
		}
	}
	return out
}

// deltaRowCount counts without building rows; forked says the lift found a
// parent, the only thing that suppresses a key.
func deltaRowCount(deltas map[string]livedoc.FormDelta, liftFork, forked bool) int {
	n := 0
	var dead map[string]bool
	for key, d := range deltas {
		if d.Event == livedoc.FormDeleted {
			if dead == nil {
				dead = map[string]bool{}
			}
			if !dead[d.Form] {
				dead[d.Form] = true
				n++
			}
			continue
		}
		if liftFork && forked && forkKey(d.Kind, strings.TrimPrefix(key, d.Form+".")) {
			continue
		}
		n++
	}
	return n
}

func anyDeltaRow(deltas map[string]livedoc.FormDelta, liftFork, forked bool) bool {
	return deltaRowCount(deltas, liftFork, forked) > 0
}

func deltaKeyWidth(rows []deltaRow) int {
	w := 0
	for _, r := range rows {
		if r.banner {
			continue
		}
		if n := runewidth.StringWidth(r.key); n > w && n <= formDeltaKeyCap {
			w = n
		}
	}
	return w
}

// One row per key: a value too long is elided, not wrapped; yank gives it back whole.
func deltaRowText(r deltaRow, keyw, width int) string {
	if r.banner {
		return truncCols(r.glyph+" "+r.key, width)
	}
	var b strings.Builder
	b.WriteString(padTo(truncCols(r.key, formDeltaKeyCap), keyw))
	b.WriteString("  ")
	from, to := cell(r.from), cell(r.to)
	if room := width - runewidth.StringWidth(b.String()) - len(deltaArrow); room > 0 {
		from, to = fitTransition(from, to, room)
	}
	b.WriteString(from)
	b.WriteString(deltaArrow)
	b.WriteString(to)
	return truncCols(b.String(), width)
}

const deltaArrow = " -> "

// The new value gets the odd column and whatever the old one does not want.
func fitTransition(from, to string, room int) (string, string) {
	fw, tw := runewidth.StringWidth(from), runewidth.StringWidth(to)
	if fw+tw <= room {
		return from, to
	}
	half := room / 2
	if fw > half && tw > room-half {
		return elide(from, half), elide(to, room-half)
	}
	if fw <= half {
		return from, elide(to, room-fw)
	}
	return elide(from, room-tw), to
}

func elide(s string, w int) string {
	if w < formDeltaValueFloor {
		w = formDeltaValueFloor
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	return truncCols(s, w-1) + "…"
}

// deltaRowFull is what the row hashes and yanks as, uncapped.
func deltaRowFull(r deltaRow) string {
	if r.banner {
		return r.glyph + " " + r.key
	}
	return r.key + "  " + cell(r.from) + deltaArrow + cell(r.to)
}

func cell(v deltaCell) string {
	if !v.present {
		return absentValue
	}
	s := flatten(v.text)
	if s == "" {
		return emptyValue
	}
	return s
}

func flatten(s string) string {
	return strings.Join(strings.Fields(sanitize(s)), " ")
}

// The row clipper passes ANSI escapes through uncounted, so a stored \x1b[2J
// would erase the display.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			if unicode.IsSpace(r) {
				return ' '
			}
			return -1
		}
		return r
	}, s)
}

type deltaGroup struct {
	kind    livedoc.FormKind
	deleted bool
	keys    []string
	byKey   map[string]livedoc.FormDelta
}

func groupDeltas(deltas map[string]livedoc.FormDelta) (map[string]*deltaGroup, []string) {
	groups := map[string]*deltaGroup{}
	for k, d := range deltas {
		g := groups[d.Form]
		if g == nil {
			g = &deltaGroup{kind: d.Kind, byKey: map[string]livedoc.FormDelta{}}
			groups[d.Form] = g
		}
		if d.Event == livedoc.FormDeleted {
			g.deleted = true
			continue
		}
		bare := strings.TrimPrefix(k, d.Form+".")
		g.keys = append(g.keys, bare)
		g.byKey[bare] = d
	}
	order := make([]string, 0, len(groups))
	for id, g := range groups {
		sort.Strings(g.keys)
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		bi, bj := groups[order[i]].kind == livedoc.FormBound, groups[order[j]].kind == livedoc.FormBound
		if bi != bj {
			return bi
		}
		return order[i] < order[j]
	})
	return groups, order
}

const forkedFromKey = "system.forked_from"

// forkParent comes off the fork's birth patch, which stamps system.forked_from.
func forkParent(g *deltaGroup) string {
	if g.kind != livedoc.FormBound {
		return ""
	}
	if d, ok := g.byKey[forkedFromKey]; ok && d.Event == livedoc.FormSet {
		return unquote(d.Value).text
	}
	return ""
}

func forkKey(kind livedoc.FormKind, key string) bool {
	return kind == livedoc.FormBound && (key == forkedFromKey || key == "aria_id")
}

// The bound board is unnamed; every other form is named by id.
func deltaFormName(formID string, g *deltaGroup) string {
	if g.kind == livedoc.FormBound {
		return ""
	}
	return formID + "."
}

// A JSON string comes back decoded; anything else as raw JSON; absent is an absent
// cell.
func unquote(raw []byte) deltaCell {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return deltaCell{}
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var decoded string
		if json.Unmarshal([]byte(s), &decoded) == nil {
			return deltaCell{text: decoded, present: true}
		}
		return deltaCell{text: s[1 : len(s)-1], present: true}
	}
	return deltaCell{text: s, present: true}
}
