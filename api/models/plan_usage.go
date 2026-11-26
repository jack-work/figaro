package models

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const PlanUsageFormID = "@plan-usage"

type PlanNode struct {
	Leaves   map[string]PlanUsage `json:"models,omitempty"`
	Branches map[string]PlanNode  `json:"upstreams,omitempty"`
}

func (n *PlanNode) Place(model string, u PlanUsage) {
	n.place(Path(model), u)
}

func (n *PlanNode) place(segs []string, u PlanUsage) {
	if len(segs) == 1 {
		if n.Leaves == nil {
			n.Leaves = map[string]PlanUsage{}
		}
		n.Leaves[segs[0]] = u
		return
	}
	if n.Branches == nil {
		n.Branches = map[string]PlanNode{}
	}
	b := n.Branches[segs[0]]
	b.place(segs[1:], u)
	n.Branches[segs[0]] = b
}

func (n PlanNode) Find(model string) (PlanUsage, bool) {
	segs := Path(model)
	for _, s := range segs[:len(segs)-1] {
		b, ok := n.Branches[s]
		if !ok {
			return PlanUsage{}, false
		}
		n = b
	}
	u, ok := n.Leaves[segs[len(segs)-1]]
	return u, ok
}

func (n PlanNode) Flat() map[string]PlanUsage {
	out := map[string]PlanUsage{}
	n.flatten("", out)
	return out
}

func (n PlanNode) flatten(prefix string, out map[string]PlanUsage) {
	for id, u := range n.Leaves {
		out[prefix+id] = u
	}
	for name, b := range n.Branches {
		b.flatten(prefix+name+RouteSep, out)
	}
}

func (n PlanNode) Empty() bool { return len(n.Leaves) == 0 && len(n.Branches) == 0 }

func PlanUsageKey(provider, model string) string { return EntryKey(provider, model) }

func PlanUsageIn(plans form.Snapshot, provider, model string) *PlanUsage {
	if provider == "" || model == "" {
		return nil
	}
	raw, ok := plans.Get(PlanUsageKey(provider, model))
	if !ok {
		return nil
	}
	var u PlanUsage
	if json.Unmarshal(raw, &u) != nil || len(u.Windows) == 0 {
		return nil
	}
	return &u
}

func (u PlanUsage) SameShown(other PlanUsage) bool {
	if u.Plan != other.Plan || len(u.Windows) != len(other.Windows) {
		return false
	}
	for i := range u.Windows {
		a, b := u.Windows[i], other.Windows[i]
		a.FetchedAt, b.FetchedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(a, b) {
			return false
		}
	}
	return true
}
