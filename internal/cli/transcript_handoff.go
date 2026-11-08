package cli

import (
	"math"
	"strings"
	"time"
)

const (
	handoffTickRate = 30
	handoffWakeRate = 60
	handoffSlack    = time.Millisecond
	handoffLevel    = 3
)

func handoffTick() time.Duration {
	if ls := activeLockstep; ls != nil {
		return 2 * time.Second / time.Duration(ls.fps)
	}
	return time.Second / handoffTickRate
}

func handoffWake() time.Duration {
	if ls := activeLockstep; ls != nil {
		return time.Second / time.Duration(ls.fps)
	}
	return time.Second / handoffWakeRate
}

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
	if t.turnRuleAt(line) {
		return turn, line
	}
	if above < len(t.stickyBlockOf(turn).rows) {
		return 0, -1
	}
	return turn, -1
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
		if h.turn != 0 && h.line >= 0 {
			h.releaseAt, h.releaseLine = now, h.line
		}
		if turn != 0 {
			h.pinAt = now
		}
	}
	h.seen, h.turn, h.line = true, turn, line
}

func (t *transcript) handoffRadius(at time.Time) (int, bool) {
	n := max(t.w, 3)
	full := dyadicReach(handoffLevel, n) + 1
	if at.IsZero() {
		return full, false
	}
	r := int((t.clock().Sub(at)+handoffSlack)/handoffTick()) + 1
	r = min(max(r, 1), full)
	return r, r < full
}

func (t *transcript) edgeRule() string {
	r, running := t.handoffRadius(t.handoff.pinAt)
	if !running {
		return t.headRule()
	}
	n := max(t.w, 3)
	return dimRuleOf(n, func(i int) bool { return dyadicDouble(handoffLevel, i, n, r) })
}

func (t *transcript) releasedRule(screen []string, top, body int) {
	h := &t.handoff
	r, running := t.handoffRadius(h.releaseAt)
	if !running {
		return
	}
	row := top + h.releaseLine - t.offset
	if row < top || row >= top+body || row >= len(screen) {
		return
	}
	if !t.turnRuleAt(h.releaseLine) || strings.Trim(pitText(screen[row]), ruleSingle) != "" {
		return
	}
	n := max(t.w, 3)
	screen[row] = dimRuleOf(n, func(i int) bool { return !dyadicDouble(handoffLevel, i, n, r) })
}

func (t *transcript) handoffRunning() bool {
	_, pin := t.handoffRadius(t.handoff.pinAt)
	_, release := t.handoffRadius(t.handoff.releaseAt)
	return pin || release
}

func (t *transcript) wakeForHandoff() {
	if t.wake == nil || t.waking || !t.handoffRunning() {
		return
	}
	t.waking = true
	t.wake(handoffWake())
}

func dyadicDouble(level, i, n, r int) bool {
	return dyadicDistance(level, i, n) < r
}

func dyadicReach(level, n int) int {
	reach := 0
	for i := range n {
		reach = max(reach, dyadicDistance(level, i, n))
	}
	return reach
}

func dyadicDistance(level, i, n int) int {
	if n < 2 {
		return 0
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
	return int(nearest)
}
