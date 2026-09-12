package cli

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/term"
)

// Form deltas, drawn as a TABLE: one row per key, Δ, the key, then the
// transition it made. No sentences. A reader of a transcript scans this
// for "what was the model shown that I cannot see", and a paragraph is
// the wrong shape for an answer with columns in it. The one exception is
// a fork, which is not a key transition at all: it gets the fork glyph
// and the parent id, alone on its row, because that row is also the
// thing `f j` jumps to.

// formDeltaValueCap bounds one value's contribution to a collapsed row.
// The wire carries studied values WHOLE (nothing in the pipeline
// truncates them); this is display only, and expansion shows everything.
const formDeltaValueCap = 24

// formDeltaKeyCap bounds the key column so one long key does not push
// every transition off the right edge.
const formDeltaKeyCap = 28

// formDeltaRowCap is how many rows the collapsed table shows. A board
// write of a dozen keys is furniture until a reader asks for it; Enter on
// the table opens the rest.
const formDeltaRowCap = 3

// forkGlyph marks the row naming the aria this one was forked from. U+2442
// is carried by FreeMono, which is the same fallback path that already
// paints the pit's 𝄚 and ♭.
const forkGlyph = "⑂"

// deltaGlyph heads every ordinary row.
const deltaGlyph = "Δ"

// deadGlyph marks the row saying a studied form's source died. A dead
// form is not a key transition either, so it takes a banner row: glyph
// and id, nothing else.
const deadGlyph = "⊘"

// absentValue stands in for the side of a transition that does not exist:
// a key being born has no old, a key removed has no new.
const absentValue = "∅"

// deltaIndent insets a delta row beneath the prose it follows, which
// render.Prose insets by the same two columns.
const deltaIndent = "  "

// deltaRow is one line of the table before it is padded and clipped.
type deltaRow struct {
	glyph  string
	key    string
	from   string
	to     string
	banner bool // a banner row carries only its glyph and an id: a fork, a death
}

// formDeltaLines renders one delta set as the table. Collapsed shows at
// most formDeltaRowCap rows with capped values and an overflow count;
// expanded (Enter on the table, or `show --details`) shows every row
// whole. Every row is clipped to the screen.
func formDeltaLines(deltas map[string]livedoc.FormDelta, width int, expanded bool) []string {
	plain := formDeltaPlain(deltas, width, expanded)
	out := make([]string, len(plain))
	for i, l := range plain {
		out[i] = term.StateDim(l)
	}
	return out
}

// formDeltaPlain is the table unstyled: what it hashes as, and what it
// yanks as. formDeltaLines is this plus the theme's state-dim role.
func formDeltaPlain(deltas map[string]livedoc.FormDelta, width int, expanded bool) []string {
	rows := deltaRows(deltas)
	if len(rows) == 0 {
		return nil
	}
	overflow := 0
	if !expanded && len(rows) > formDeltaRowCap {
		overflow = len(rows) - formDeltaRowCap
		rows = rows[:formDeltaRowCap]
	}
	keyw := 0
	for _, r := range rows {
		if r.banner {
			continue
		}
		if n := runewidth.StringWidth(r.key); n > keyw && n <= formDeltaKeyCap {
			keyw = n
		}
	}
	out := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		var b strings.Builder
		b.WriteString(deltaIndent)
		b.WriteString(r.glyph)
		b.WriteString(" ")
		if r.banner {
			b.WriteString(r.key)
			out = append(out, truncCols(b.String(), width))
			continue
		}
		b.WriteString(padTo(truncCols(r.key, formDeltaKeyCap), keyw))
		b.WriteString("  ")
		b.WriteString(cell(r.from, expanded))
		b.WriteString(" -> ")
		b.WriteString(cell(r.to, expanded))
		out = append(out, truncCols(b.String(), width))
	}
	if overflow > 0 {
		out = append(out, truncCols(deltaIndent+"⋯ +"+strconv.Itoa(overflow), width))
	}
	return out
}

// cell renders one side of a transition: single-lined always, capped when
// the table is collapsed.
func cell(v string, expanded bool) string {
	if v == "" {
		return absentValue
	}
	v = flatten(v)
	if !expanded {
		v = truncCols(v, formDeltaValueCap)
	}
	return v
}

// flatten puts a multi-line value on one row. A form value may be a whole
// credo; the table is a table.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// deltaRows orders the table: the fork row first (it is the banner), then
// this figaro's own board, then every other form by id, keys sorted.
func deltaRows(deltas map[string]livedoc.FormDelta) []deltaRow {
	if len(deltas) == 0 {
		return nil
	}
	groups, order := groupDeltas(deltas)
	var out []deltaRow
	for _, formID := range order {
		g := groups[formID]
		if parent := forkParent(g); parent != "" {
			out = append(out, deltaRow{glyph: forkGlyph, key: parent, banner: true})
		}
	}
	for _, formID := range order {
		g := groups[formID]
		prefix := deltaFormName(formID, g)
		if g.deleted {
			out = append(out, deltaRow{glyph: deadGlyph, key: formID, banner: true})
		}
		for _, k := range g.keys {
			if forkKey(g.kind, k) && forkParent(g) != "" {
				continue
			}
			d := g.byKey[k]
			row := deltaRow{glyph: deltaGlyph, key: prefix + k, from: unquote(d.Prev)}
			if d.Event != livedoc.FormRemoved {
				row.to = unquote(d.Value)
			}
			out = append(out, row)
		}
	}
	return out
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
		return unquote(d.Value)
	}
	return ""
}

// forkKeys are consumed by the fork row and suppressed from the table, or
// the banner and its raw material both draw.
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

// unquote renders a raw JSON value for a table cell: a JSON string comes
// back as its text (escapes decoded, so a credo's newlines are newlines
// and flatten can fold them), anything else as the raw JSON it is.
func unquote(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var decoded string
		if json.Unmarshal([]byte(s), &decoded) == nil {
			return decoded
		}
		return s[1 : len(s)-1]
	}
	return s
}

// deltasExpandable reports whether the expanded table would show more
// than the collapsed one did: rows held back, or a capped value.
func deltasExpandable(deltas map[string]livedoc.FormDelta, width int) bool {
	if len(deltas) == 0 {
		return false
	}
	collapsed := formDeltaLines(deltas, width, false)
	expanded := formDeltaLines(deltas, width, true)
	if len(expanded) != len(collapsed) {
		return true
	}
	for i := range expanded {
		if expanded[i] != collapsed[i] {
			return true
		}
	}
	return false
}
