package cli

import (
	"math"
	"strings"
	"time"
)

const (
	handoffLength = 220 * time.Millisecond
	handoffFrame  = time.Second / 60
	handoffLevel  = 3
)

type ruleHandoff struct {
	seen        bool
	turn        int
	line        int
	pinAt       time.Time
	releaseAt   time.Time
	releaseLine int
}

func (t *transcript) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return clk.Now()
}

func (t *transcript) stickyEdge() (turn, line int) {
	if t.head.open {
		return 0, -1
	}
	rows := t.stickyRows()
	if len(rows) == 0 {
		return 0, -1
	}
	turn, above := t.stickyTurn()
	line = t.offset + len(rows)
	if above < len(t.stickyBlockOf(turn).rows) && !t.turnRuleAt(line) {
		return 0, -1
	}
	return turn, line
}

func (t *transcript) turnRuleAt(line int) bool {
	if line >= t.index.total {
		return false
	}
	text := strings.TrimSpace(pitText(t.lineAt(line)))
	return text != "" && strings.Trim(text, ruleSingle) == ""
}

func (t *transcript) noteHandoff() {
	turn, line := t.stickyEdge()
	h := &t.handoff
	if h.seen && turn != h.turn {
		now := t.clock()
		if h.turn != 0 {
			h.releaseAt, h.releaseLine = now, h.line
		}
		if turn != 0 {
			h.pinAt = now
		}
	}
	h.seen, h.turn, h.line = true, turn, line
}

func (t *transcript) handoffProgress(at time.Time) (float64, bool) {
	if at.IsZero() {
		return 1, false
	}
	p := float64(t.clock().Sub(at)) / float64(handoffLength)
	return math.Min(math.Max(p, 0), 1), p < 1
}

func (t *transcript) edgeRule() string {
	p, running := t.handoffProgress(t.handoff.pinAt)
	if !running {
		return t.headRule()
	}
	n := max(t.w, 3)
	return dimRuleOf(n, func(i int) bool { return dyadicDouble(handoffLevel, i, n, p) })
}

func (t *transcript) releasedRule(screen []string, top, body int) {
	h := &t.handoff
	p, running := t.handoffProgress(h.releaseAt)
	if !running {
		return
	}
	row := top + h.releaseLine - t.offset
	if row < top || row >= top+body || row >= len(screen) {
		return
	}
	if strings.Trim(pitText(screen[row]), ruleSingle) != "" {
		return
	}
	n := max(t.w, 3)
	screen[row] = dimRuleOf(n, func(i int) bool { return !dyadicDouble(handoffLevel, i, n, p) })
}

func (t *transcript) handoffRunning() bool {
	_, pin := t.handoffProgress(t.handoff.pinAt)
	_, release := t.handoffProgress(t.handoff.releaseAt)
	return pin || release
}

func (t *transcript) wakeForHandoff() {
	if t.wake == nil || t.waking || !t.handoffRunning() {
		return
	}
	t.waking = true
	t.wake(handoffFrame)
}

func dyadicDouble(level, i, n int, p float64) bool {
	if p <= 0 {
		return false
	}
	if p >= 1 || n < 2 {
		return true
	}
	intervals := 1 << level
	spacing := float64(n-1) / float64(intervals)
	j := int(math.Round(float64(i) / spacing))
	nearest := math.Inf(1)
	for _, s := range []int{j - 1, j, j + 1} {
		if s < 0 || s > intervals {
			continue
		}
		seed := math.Round(float64(s) * spacing)
		nearest = math.Min(nearest, math.Abs(float64(i)-seed))
	}
	return nearest <= p*spacing/2
}
