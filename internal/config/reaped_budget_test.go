package config

import (
	"testing"
	"time"
)

func memoryWith(budgetMB, graceHours, minHours *int) *Loaded {
	var l Loaded
	l.Config.Memory.ReapedBudgetMB = budgetMB
	l.Config.Memory.ReapedGraceHours = graceHours
	l.Config.Memory.ReapedMinHours = minHours
	return &l
}

func hours(n int) *int { return &n }

func TestWithoutABudgetTheReapedGraceKeepsItsMeaning(t *testing.T) {
	for name, c := range map[string]struct {
		grace *int
		want  time.Duration
	}{
		"unset is a week":       {nil, 7 * 24 * time.Hour},
		"0 deletes at once":     {hours(0), 0},
		"720 waits thirty days": {hours(720), 720 * time.Hour},
	} {
		l := memoryWith(nil, c.grace, nil)
		got, capped := l.ReapedAgeCap()
		if !capped || got != c.want {
			t.Errorf("%s: cap %v capped=%v, want %v capped", name, got, capped, c.want)
		}
		if l.ReapedBudget() != 0 {
			t.Errorf("%s: a budget appeared from nowhere", name)
		}
	}
}

func TestABudgetMakesTheGraceAnOptionalAgeCap(t *testing.T) {
	if _, capped := memoryWith(hours(2048), nil, nil).ReapedAgeCap(); capped {
		t.Error("with a budget and no grace, age must not purge")
	}
	if _, capped := memoryWith(hours(2048), hours(0), nil).ReapedAgeCap(); capped {
		t.Error("with a budget, grace 0 is no cap, not delete at once")
	}
	if got, capped := memoryWith(hours(2048), hours(2160), nil).ReapedAgeCap(); !capped || got != 2160*time.Hour {
		t.Errorf("with a budget, a set grace still caps age: got %v capped=%v", got, capped)
	}
	if got := memoryWith(hours(2048), nil, nil).ReapedBudget(); got != 2048<<20 {
		t.Errorf("budget %d bytes, want 2 GiB", got)
	}
	if got := memoryWith(hours(-1), nil, nil).ReapedBudget(); got != 0 {
		t.Errorf("a negative budget is no budget, got %d", got)
	}
}

func TestTheReapedFloorDefaultsToADay(t *testing.T) {
	if got := memoryWith(hours(1), nil, nil).ReapedMinAge(); got != 24*time.Hour {
		t.Errorf("unset floor %v, want a day", got)
	}
	if got := memoryWith(hours(1), nil, hours(0)).ReapedMinAge(); got != 0 {
		t.Errorf("floor 0 %v, want none", got)
	}
	if got := memoryWith(hours(1), nil, hours(72)).ReapedMinAge(); got != 72*time.Hour {
		t.Errorf("floor %v, want three days", got)
	}
}
