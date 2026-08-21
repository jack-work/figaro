package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/term"
)

// Form deltas, drawn. One line per form, grouped by form id -- not one
// line per key, or a board write of six keys becomes six lines of
// furniture -- in the theme's state-dim role, below the node they explain,
// truncated to the width of the screen. Two of those deltas render as
// SENTENCES instead of key/value pairs: a fork and a recast are events a
// reader narrates, not state they inspect.

// formDeltaValueCap bounds one value's contribution to the collapsed
// line. The wire carries studied values WHOLE (nothing in the pipeline
// truncates them); this is display only, and expansion shows everything.
const formDeltaValueCap = 24

// deltaIndent insets a delta row beneath the prose it follows, which
// render.Prose insets by the same two columns.
const deltaIndent = "  "

// formDeltaLines renders one delta set: one row per key, each in the
// "Figaro saw:" voice -- these rows answer "what was the model shown that
// I cannot see", and the sentence should say so. Collapsed caps each
// value; expanded (Enter on the node, or `show --details`) shows it
// whole. Every row is clipped to the screen.
func formDeltaLines(deltas map[string]livedoc.FormDelta, width int, expanded bool) []string {
	if len(deltas) == 0 {
		return nil
	}
	groups, order := groupDeltas(deltas)
	var out []string
	for _, formID := range order {
		g := groups[formID]
		if s := deltaSentence(formID, g); s != "" {
			out = append(out, term.StateDim(truncCols(deltaIndent+s, width)))
			if !hasOrdinaryKeys(g) && !g.deleted {
				continue
			}
		}
		subject := deltaFormName(formID, g)
		if g.deleted {
			out = append(out, term.StateDim(truncCols(deltaIndent+"∆ Figaro saw: "+strings.TrimSpace(subject)+" deleted", width)))
		}
		for _, k := range g.keys {
			if sentenceKey(g.kind, k) && deltaSentence(formID, g) != "" {
				continue
			}
			d := g.byKey[k]
			var line string
			switch {
			case d.Event == livedoc.FormRemoved:
				line = deltaIndent + "∆ Figaro saw: " + subject + k + " removed"
			case expanded:
				line = deltaIndent + "∆ Figaro saw: " + subject + k + " -> " + string(d.Value)
			default:
				line = deltaIndent + "∆ Figaro saw: " + subject + k + " -> " + truncCols(string(d.Value), formDeltaValueCap)
			}
			out = append(out, term.StateDim(truncCols(line, width)))
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

// deltaSentence renders the two events that read as prose: a fork of this
// figaro, and a recast of a role. Empty when the group is ordinary state.
func deltaSentence(formID string, g *deltaGroup) string {
	if g.kind == livedoc.FormBound {
		// A fork's birth patch stamps system.forked_from beside the new
		// aria_id, so the sentence names the parent from the delta itself.
		if d, ok := g.byKey["system.forked_from"]; ok && d.Event == livedoc.FormSet {
			return fmt.Sprintf("· this figaro has been forked from %s", unquote(d.Value))
		}
		return ""
	}
	if g.kind == livedoc.FormRole {
		if d, ok := g.byKey["target-aria"]; ok && d.Event == livedoc.FormSet {
			return fmt.Sprintf("· role %s recast to figaro %s", formID, unquote(d.Value))
		}
	}
	return ""
}

// sentenceKeys are consumed by deltaSentence and suppressed from the
// key/value rendering, or the sentence and its raw material both draw.
func sentenceKey(kind livedoc.FormKind, key string) bool {
	switch kind {
	case livedoc.FormBound:
		return key == "system.forked_from" || key == "aria_id"
	case livedoc.FormRole:
		return key == "target-aria"
	}
	return false
}

func hasOrdinaryKeys(g *deltaGroup) bool {
	for _, k := range g.keys {
		if !sentenceKey(g.kind, k) {
			return true
		}
	}
	return false
}

// deltaFormName is the SUBJECT prefix of a "Figaro saw:" row: the bound
// board says nothing (the row is about this figaro's own state), a role is
// named as one, and a studied form is its id. Non-empty forms carry a
// trailing space so the key can be concatenated directly.
func deltaFormName(formID string, g *deltaGroup) string {
	switch g.kind {
	case livedoc.FormBound:
		return ""
	case livedoc.FormRole:
		return "role " + formID + " "
	default:
		return formID + " "
	}
}

func unquote(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// deltasExpandable reports whether the expanded form would show more than
// the collapsed one did: more than one row's worth of keys, a capped
// value, or a second form.
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
