package angelus

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

const mb = 1 << 20

func chosen(over map[string]bool) []string {
	return slices.Sorted(maps.Keys(over))
}

func reapedAgo(now time.Time, d time.Duration) int64 { return now.Add(-d).UnixMilli() }

func TestTheBudgetPurgesOldestReapedFirstUntilUnder(t *testing.T) {
	now := time.Now()
	groups := []budgetGroup{
		{root: "newest", atMS: reapedAgo(now, 2*24*time.Hour), bytes: 40 * mb},
		{root: "oldest", atMS: reapedAgo(now, 20*24*time.Hour), bytes: 30 * mb},
		{root: "middle", atMS: reapedAgo(now, 10*24*time.Hour), bytes: 30 * mb},
	}
	if got := chosen(chooseOverBudget(groups, 50*mb, 0, now)); !slices.Equal(got, []string{"middle", "oldest"}) {
		t.Errorf("chose %v, want the two oldest: 100 MB less 60 is under 50 only after both", got)
	}
	if got := chosen(chooseOverBudget(groups, 70*mb, 0, now)); !slices.Equal(got, []string{"oldest"}) {
		t.Errorf("chose %v, want the oldest alone", got)
	}
	if got := chooseOverBudget(groups, 100*mb, 0, now); len(got) != 0 {
		t.Errorf("chose %v at the budget exactly, want nothing", chosen(got))
	}
}

func TestNothingYoungerThanTheFloorIsPurgedForSize(t *testing.T) {
	now := time.Now()
	groups := []budgetGroup{
		{root: "old", atMS: reapedAgo(now, 3*24*time.Hour), bytes: 10 * mb},
		{root: "fresh", atMS: reapedAgo(now, time.Hour), bytes: 500 * mb},
	}
	if got := chosen(chooseOverBudget(groups, 100*mb, 24*time.Hour, now)); !slices.Equal(got, []string{"old"}) {
		t.Errorf("chose %v, want only the group past the floor, even though it cannot bring the total under", got)
	}
}

func TestAKillDeadlineIsCountedButNeverMovedByTheBudget(t *testing.T) {
	now := time.Now()
	groups := []budgetGroup{
		{root: "killed", atMS: reapedAgo(now, 30*24*time.Hour), bytes: 80 * mb, killed: true},
		{root: "swept", atMS: reapedAgo(now, 5*24*time.Hour), bytes: 40 * mb},
	}
	if got := chosen(chooseOverBudget(groups, 100*mb, 0, now)); !slices.Equal(got, []string{"swept"}) {
		t.Errorf("chose %v, want the swept group: the killed one keeps its deadline but still takes room", got)
	}
}

func TestAGroupTheAgeCapTakesFreesItsRoomForTheRest(t *testing.T) {
	now := time.Now()
	groups := []budgetGroup{
		{root: "capped", atMS: reapedAgo(now, 90*24*time.Hour), bytes: 80 * mb, capped: true},
		{root: "kept", atMS: reapedAgo(now, 5*24*time.Hour), bytes: 40 * mb},
	}
	if got := chooseOverBudget(groups, 100*mb, 0, now); len(got) != 0 {
		t.Errorf("chose %v, want nothing: the age cap already purges 80 MB", chosen(got))
	}
}

func TestAHeldGroupStaysChosenAndTheNextOldestGoesToo(t *testing.T) {
	now := time.Now()
	groups := []budgetGroup{
		{root: "held", atMS: reapedAgo(now, 20*24*time.Hour), bytes: 60 * mb, held: true},
		{root: "next", atMS: reapedAgo(now, 10*24*time.Hour), bytes: 60 * mb},
		{root: "young", atMS: reapedAgo(now, 2*24*time.Hour), bytes: 10 * mb},
	}
	if got := chosen(chooseOverBudget(groups, 100*mb, 0, now)); !slices.Equal(got, []string{"held", "next"}) {
		t.Errorf("chose %v, want the held group retried and the next oldest taken in its place", got)
	}
}

func TestABudgetPickReapedAgainWithinTheFloorIsNotPurged(t *testing.T) {
	budget := 1
	a := &Angelus{Settings: &config.Loaded{Config: config.Config{Memory: config.MemoryConfig{ReapedBudgetMB: &budget}}}}
	a.budget.over = map[string]bool{"r": true}
	now := time.Now()
	if a.pastGrace(reapedGroup{root: "r", atMS: reapedAgo(now, time.Hour)}, now) {
		t.Error("a group restored and reaped again an hour ago was purged on the last sweep's pick")
	}
	if !a.pastGrace(reapedGroup{root: "r", atMS: reapedAgo(now, 48*time.Hour)}, now) {
		t.Error("a pick past the floor was not purged")
	}
}
