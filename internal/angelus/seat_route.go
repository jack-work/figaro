package angelus

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var seatRouteKeys = []string{
	"system.provider",
	"system.model",
	"system.max_tokens",
	"system.thinking_effort",
	"system.thinking_budget",
	"system.methods",
	"system.decide",
}

func seatRoute(snap form.Snapshot) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	copied(snap, out, seatRouteKeys...)
	return out
}

func seatOutfits(snap form.Snapshot) []string {
	o := nested(snap, successionPolicyKey, "outfit")
	if o == "" {
		return nil
	}
	return strings.Split(o, ",")
}

func successorRoute(roles []heldRole) (map[string]json.RawMessage, error) {
	route, from := map[string]json.RawMessage{}, map[string]string{}
	for _, r := range roles {
		for k, v := range r.route {
			have, ok := route[k]
			switch {
			case !ok:
				route[k], from[k] = v, r.id
			case !jsonEqual(have, v):
				return nil, fmt.Errorf("roles disagree on the successor's %s: %s wants %s, %s wants %s", k, from[k], have, r.id, v)
			}
		}
	}
	return route, nil
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".")
}

func patchCovers(p form.Patch, key string) bool {
	return slices.ContainsFunc(p.Entries(), func(e form.Entry) bool { return overlaps(e.Key, key) })
}

func (h *handlers) dressForSeat(req rpc.ForkRequest) (rpc.ForkRequest, error) {
	snap, err := h.angelus.Backend.FormState(req.Seat)
	if err != nil {
		return req, fmt.Errorf("fork: seat %s: %w", req.Seat, err)
	}
	if v := snap.Lookup("target-aria"); v == nil || *v != req.FigaroID {
		return req, fmt.Errorf("fork: seat %s does not target %s", req.Seat, req.FigaroID)
	}
	var caller form.Patch
	if req.Patch != nil {
		caller = *req.Patch
	}
	callerOutfits, err := h.dress(req.Outfits, nil)
	if err != nil {
		return req, err
	}
	set := map[string]json.RawMessage{}
	for k, v := range seatRoute(snap) {
		if !patchCovers(callerOutfits, k) && !patchCovers(caller, k) {
			set[k] = v
		}
	}
	var drops []string
	for _, e := range caller.Entries() {
		if e.IsRemoval() {
			drops = append(drops, e.Key)
		} else {
			set[e.Key] = e.New
		}
	}
	patch := form.Build(form.Snapshot{}, set, drops)
	req.Patch = &patch
	req.Outfits = append(seatOutfits(snap), req.Outfits...)
	return req, nil
}
