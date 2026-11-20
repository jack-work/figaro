package angelus

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
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

func routeOf(snap form.Snapshot) (map[string]json.RawMessage, []string) {
	set := map[string]json.RawMessage{}
	copied(snap, set, form.RouteKeys...)
	var drops []string
	for _, k := range form.RouteKeys {
		if _, ok := set[k]; !ok {
			drops = append(drops, k)
		}
	}
	return set, drops
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

func seatProviderAlone(route map[string]json.RawMessage) (string, bool) {
	if _, named := route["system.model"]; named {
		return "", false
	}
	var p string
	if json.Unmarshal(route["system.provider"], &p) != nil || p == "" {
		return "", false
	}
	return p, true
}

func orphanedModel(seat, under string) (bool, error) {
	if seat == under {
		return false, nil
	}
	if reg := providerPkg.Lookup(seat); reg != nil && (reg.Declared || reg.DefaultModel != "") {
		return true, nil
	}
	return false, fmt.Errorf("the seat names provider %s and no model, the successor's model belongs to %q, and %s has no default model: set system.model on the role", seat, under, seat)
}

func seatProviderPatch(seat string, dropModel bool) form.Patch {
	raw, _ := json.Marshal(seat)
	var drops []string
	if dropModel {
		drops = []string{"system.model"}
	}
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.provider": raw}, drops)
}

func (h *handlers) fixSeatProvider(child, seat string) error {
	snap, err := h.angelus.Backend.FormState(child)
	if err != nil {
		return err
	}
	under := ""
	if v := snap.Lookup("system.provider"); v != nil {
		under = *v
	}
	drop, err := orphanedModel(seat, under)
	if err != nil {
		return err
	}
	_, err = h.angelus.Backend.ApplyFormPrivileged(child, seatProviderPatch(seat, drop))
	return err
}

func (h *handlers) dressForSeat(req rpc.ForkRequest) (rpc.ForkRequest, string, error) {
	snap, err := h.angelus.Backend.FormState(req.Seat)
	if err != nil {
		return req, "", fmt.Errorf("fork: seat %s: %w", req.Seat, err)
	}
	if v := snap.Lookup("target-aria"); v == nil || *v != req.FigaroID {
		return req, "", fmt.Errorf("fork: seat %s does not target %s", req.Seat, req.FigaroID)
	}
	var caller form.Patch
	if req.Patch != nil {
		caller = *req.Patch
	}
	callerOutfits, err := h.dress(req.Outfits, nil)
	if err != nil {
		return req, "", err
	}
	roleOutfits, err := h.dress(seatOutfits(snap), nil)
	if err != nil {
		return req, "", err
	}
	holder, err := h.angelus.Backend.FormState(req.FigaroID)
	if err != nil {
		return req, "", fmt.Errorf("fork: %s: %w", req.FigaroID, err)
	}
	route := seatRoute(snap)
	deferred, alone := seatProviderAlone(route)
	for _, p := range []form.Patch{callerOutfits, caller} {
		if patchCovers(p, "system.model") || patchCovers(p, "system.provider") {
			deferred, alone = "", false
		}
	}
	if alone {
		delete(route, "system.provider")
	}
	set := map[string]json.RawMessage{}
	for k, v := range route {
		if !patchCovers(callerOutfits, k) && !patchCovers(caller, k) {
			set[k] = v
		}
	}
	kept, _ := routeOf(holder)
	var drops []string
	for _, k := range form.RouteKeys {
		if !patchCovers(roleOutfits, k) || patchCovers(callerOutfits, k) || patchCovers(caller, k) {
			continue
		}
		if _, routed := set[k]; routed {
			continue
		}
		if v, ok := kept[k]; ok {
			set[k] = v
		} else {
			drops = append(drops, k)
		}
	}
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
	return req, deferred, nil
}

func withHolderLifetime(dress form.Patch, holder form.Snapshot) form.Patch {
	ttl, ok := holder.Get(store.SystemTTLKey)
	if !ok {
		ttl = json.RawMessage(`"0"`)
	}
	set := map[string]json.RawMessage{store.SystemTTLKey: ttl}
	var drop []string
	if ka, ok := holder.Get(store.SystemKeepaliveKey); ok {
		set[store.SystemKeepaliveKey] = ka
	} else {
		drop = append(drop, store.SystemKeepaliveKey)
	}
	return form.Merge(dress, form.Build(form.Snapshot{}, set, drop))
}
