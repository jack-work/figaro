package angelus

import (
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

func (r *Registry) ids() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.figaros))
	for id := range r.figaros {
		out = append(out, id)
	}
	return out
}

// Residents are the live agents and open endpoints whose id the store holds
// neither as a node nor as a form: what a delete should have taken with it.
func (a *Angelus) Residents() []rpc.Resident {
	if a.Backend == nil {
		return []rpc.Resident{}
	}
	byID := map[string]*rpc.Resident{}
	gone := func(id string) *rpc.Resident {
		if r, ok := byID[id]; ok {
			return r
		}
		if _, held := a.Backend.Node(id); held {
			return nil
		}
		if _, err := a.Backend.FormState(id); err == nil {
			return nil
		}
		byID[id] = &rpc.Resident{ID: id}
		return byID[id]
	}
	if a.Registry != nil {
		for _, id := range a.Registry.ids() {
			if r := gone(id); r != nil {
				r.Agent = true
			}
		}
	}
	if a.Hubs != nil {
		for _, id := range a.Hubs.ids() {
			if r := gone(id); r != nil {
				r.Endpoint = true
			}
		}
	}
	out := make([]rpc.Resident, 0, len(byID))
	for _, r := range byID {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(x, y rpc.Resident) int { return strings.Compare(x.ID, y.ID) })
	return out
}
