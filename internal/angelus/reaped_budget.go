package angelus

import (
	"log/slog"
	"maps"
	"sort"
	"sync"
	"time"
)

type budgetGroup struct {
	root   string
	atMS   int64
	bytes  int64
	killed bool
	held   bool
	capped bool
}

func chooseOverBudget(groups []budgetGroup, budget int64, floor time.Duration, now time.Time) map[string]bool {
	var total int64
	for _, g := range groups {
		if !g.capped || g.held {
			total += g.bytes
		}
	}
	if total <= budget {
		return nil
	}
	var oldest []budgetGroup
	for _, g := range groups {
		if g.capped || g.killed || g.atMS == 0 || now.Sub(time.UnixMilli(g.atMS)) < floor {
			continue
		}
		oldest = append(oldest, g)
	}
	sort.Slice(oldest, func(i, j int) bool {
		if oldest[i].atMS != oldest[j].atMS {
			return oldest[i].atMS < oldest[j].atMS
		}
		return oldest[i].root < oldest[j].root
	})
	out := map[string]bool{}
	for _, g := range oldest {
		if total <= budget {
			break
		}
		out[g.root] = true
		if !g.held {
			total -= g.bytes
		}
	}
	return out
}

type reapedBudget struct {
	mu      sync.Mutex
	over    map[string]bool
	changed func()
}

func (a *Angelus) onBudgetChange(fn func()) {
	a.budget.mu.Lock()
	defer a.budget.mu.Unlock()
	a.budget.changed = fn
}

func (a *Angelus) overBudget(root string) bool {
	a.budget.mu.Lock()
	defer a.budget.mu.Unlock()
	return a.budget.over[root]
}

func (a *Angelus) overBudgetSet() map[string]bool {
	a.budget.mu.Lock()
	defer a.budget.mu.Unlock()
	return a.budget.over
}

func (a *Angelus) planReapedBudget(groups []reapedGroup, now time.Time) {
	budget := a.Settings.ReapedBudget()
	var over map[string]bool
	if budget > 0 {
		sizes := a.reapedGroupBytes(groups)
		in := make([]budgetGroup, 0, len(groups))
		for _, g := range groups {
			_, held := a.held.get(g.root)
			in = append(in, budgetGroup{
				root: g.root, atMS: g.atMS, bytes: sizes[g.root],
				killed: g.killed(), held: held, capped: a.pastAgeCap(g, now),
			})
		}
		over = chooseOverBudget(in, budget, a.Settings.ReapedMinAge(), now)
		if len(over) > 0 {
			slog.Debug("reaped figaros over the budget will be purged, oldest first",
				"groups", len(over), "budget_mb", budget>>20)
		}
	}
	a.budget.mu.Lock()
	same := maps.Equal(a.budget.over, over)
	a.budget.over = over
	changed := a.budget.changed
	a.budget.mu.Unlock()
	if !same && changed != nil {
		changed()
	}
}
