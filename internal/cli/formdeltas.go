package cli

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
)

// Form deltas as ROWS: one row per key, each a pseudonode of its own. What
// draws them beside a block is the adornment (adornment.go); this file is
// the rows themselves, ordered and worded.

// formDeltaKeyCap bounds the key column so one long key does not push
// every transition off the right edge.
const formDeltaKeyCap = 28

// formDeltaValueFloor is the narrowest a value is squeezed to before the row
// gives up on showing both sides of the transition. Below it the row says
// nothing a reader can use.
const formDeltaValueFloor = 6

// forkGlyph names the aria this one was forked from, beside the parent id in
// the question's header row. U+2442 is carried by FreeMono, which is the same
// fallback path that already paints the pit's 𝄚 and ♭.
const forkGlyph = "⑂"

// deltaGlyph is the adornment's marker, and the head of its snake.
const deltaGlyph = "Δ"

// deadGlyph marks the row saying a studied form's source died. A dead
// form is not a key transition either, so it takes a banner row: glyph
// and id, nothing else.
const deadGlyph = "⊘"

// absentValue stands in for the side of a transition that does not exist:
// a key being born has no old, a key removed has no new.
const absentValue = "∅"

// emptyValue is a key that exists and holds the empty string.
const emptyValue = `""`

// deltaRow is one delta: one key's transition, or a banner (a fork, a
// death) that carries a glyph and an id and nothing else.
type deltaRow struct {
	glyph  string // banner rows only
	key    string
	from   deltaCell
	to     deltaCell
	banner bool
}

// deltaCell is one side of a transition. Absence and the empty string are
// different rows: ∅ means the key did not exist.
type deltaCell struct {
	text    string
	present bool
}

// deltaRows orders the rows: the fork banner first when it is lifted out of
// the list, then this figaro's own board, then every other form by id, keys
// sorted.
//
// liftFork is the INQUIRY's: a fork happens on the turn's question and
// nowhere else (see forkParent), so there the glyph and the parent id rise
// into the header row and leave the list. On any other block a forked_from
// delta is a delta like any other.
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
			// A LIFTED FORK LEAVES NO ROWS BEHIND. The glyph and the parent
			// id ride the question's header instead, and the keys the fork
			// patch wrote are what the header is made of; drawing them again
			// would say the same thing twice. Anywhere else a forked_from is
			// a key like any other.
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

// deltaKeyWidth is the key column shared by a set of rows.
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

// deltaRowText is one row as the reader sees it: the key, then the
// transition, fitted to width columns. ONE ROW PER KEY IS THE WHOLE POINT,
// so a value too long for the space it has is elided rather than wrapped;
// yanking the row gives it back whole (see deltaRowFull).
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

// deltaArrow separates the two sides of a transition.
const deltaArrow = " -> "

// fitTransition shares room between the two sides of a transition. The NEW
// value gets the odd column and whatever the old one does not want: a
// reader is reading forward.
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

// elide truncates to w columns, spending the last one on an ellipsis so the
// row says that it dropped something.
func elide(s string, w int) string {
	if w < formDeltaValueFloor {
		w = formDeltaValueFloor
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	return truncCols(s, w-1) + "…"
}

// deltaRowFull is one row whole: nothing capped, nothing elided. It is what
// the row HASHES as and what it YANKS as, so a value that differs past the
// column the screen ran out of is a different row.
func deltaRowFull(r deltaRow) string {
	if r.banner {
		return r.glyph + " " + r.key
	}
	return r.key + "  " + cell(r.from) + deltaArrow + cell(r.to)
}

// cell renders one side of a transition, single-lined.
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

// flatten puts a multi-line value on one row.
func flatten(s string) string {
	return strings.Join(strings.Fields(sanitize(s)), " ")
}

// sanitize drops control characters from untrusted form text. The row
// clipper passes ANSI escapes through uncounted, so a stored \x1b[2J would
// otherwise erase the display.
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

// deltaGroup is one form's slice of a delta set, keys bare (form prefix
// stripped), plus whether the form itself died in this window.
type deltaGroup struct {
	kind    livedoc.FormKind
	deleted bool
	keys    []string // sorted bare keys
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
	// The bound board first -- it is this figaro's own state -- then the
	// rest by id, stably.
	sort.Slice(order, func(i, j int) bool {
		bi, bj := groups[order[i]].kind == livedoc.FormBound, groups[order[j]].kind == livedoc.FormBound
		if bi != bj {
			return bi
		}
		return order[i] < order[j]
	})
	return groups, order
}

// forkParent names the aria this figaro was forked from, when the window
// holds a fork's birth patch. A fork's birth patch stamps
// system.forked_from beside the new aria_id, so the parent comes off the
// delta itself.
func forkParent(g *deltaGroup) string {
	if g.kind != livedoc.FormBound {
		return ""
	}
	if d, ok := g.byKey["system.forked_from"]; ok && d.Event == livedoc.FormSet {
		return unquote(d.Value).text
	}
	return ""
}

// forkKeys are consumed by the lifted fork and suppressed from the list, or
// the header and its raw material both draw.
func forkKey(kind livedoc.FormKind, key string) bool {
	return kind == livedoc.FormBound && (key == "system.forked_from" || key == "aria_id")
}

// deltaFormName prefixes a key with the form it belongs to. The bound
// board says nothing (the row is about this figaro's own state); every
// other form is named by id, dotted onto the key.
func deltaFormName(formID string, g *deltaGroup) string {
	if g.kind == livedoc.FormBound {
		return ""
	}
	return formID + "."
}

// unquote renders a raw JSON value for a row: a JSON string comes back as
// its text (escapes decoded, so a credo's newlines are newlines and flatten
// can fold them), anything else as the raw JSON it is. An absent raw value
// is an absent cell, distinct from an empty string.
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
