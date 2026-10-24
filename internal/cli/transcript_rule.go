package cli

import (
	"strings"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

const (
	ruleSingle = "─"
	ruleDouble = "═"
)

type junctions struct{ up, down, cross string }

var junctionsOf = map[string]junctions{
	ruleSingle: {"┴", "┬", "┼"},
	ruleDouble: {"╧", "╤", "╪"},
}

func (t *transcript) headRule() string {
	if t.headRuleStr == "" || t.headRuleW != t.w {
		t.headRuleStr, t.headRuleW = dimRuleOf(max(t.w, 3), func(int) bool { return true }), t.w
	}
	return t.headRuleStr
}

func dimRuleOf(w int, double func(i int) bool) string {
	var b strings.Builder
	b.Grow(w*3 + 8)
	b.WriteString("\x1b[2m")
	for i := range w {
		if double(i) {
			b.WriteString(ruleDouble)
		} else {
			b.WriteString(ruleSingle)
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// A stroke only where the line goes on past the rule; a gutter that merely begins
// or ends there is already whole.
func joinRule(rule string, above, below gutterCut) string {
	up, upOK := above.column()
	down, downOK := below.column()
	w := displayWidth(rule)
	upOK = upOK && up < w
	downOK = downOK && down < w
	switch {
	case upOK && downOK && up == down:
		return ldrender.OverlayColumn(rule, up, ruleJoint(rule, up).cross)
	case upOK && downOK:
		rule = ldrender.OverlayColumn(rule, up, ruleJoint(rule, up).up)
		return ldrender.OverlayColumn(rule, down, ruleJoint(rule, down).down)
	case upOK:
		return ldrender.OverlayColumn(rule, up, ruleJoint(rule, up).up)
	case downOK:
		return ldrender.OverlayColumn(rule, down, ruleJoint(rule, down).down)
	}
	return rule
}

func ruleJoint(rule string, col int) junctions {
	if cellAt(rule, col) == ruleDouble {
		return junctionsOf[ruleDouble]
	}
	return junctionsOf[ruleSingle]
}

func cellAt(row string, col int) string {
	i := 0
	for _, r := range pitText(row) {
		if i == col {
			return string(r)
		}
		i++
	}
	return ""
}
