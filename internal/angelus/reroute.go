package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func (h *handlers) reroute(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.RerouteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.FigaroID == "" || len(req.Outfits) == 0 {
		return nil, fmt.Errorf("reroute: a figaro and outfit names are required")
	}
	id, err := h.canonicalID(ctx, req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("reroute: %w", err)
	}
	if err := h.allowed(ctx, rpc.VerbSet, authz.Aria(id)); err != nil {
		return nil, err
	}
	if err := h.refuseRerouteBy(ctx, id); err != nil {
		return nil, err
	}
	patch, err := h.dressRoute(req.Outfits, req.Pins)
	if err != nil {
		return nil, err
	}
	setParams, err := json.Marshal(rpc.SetRequest{Patch: patch})
	if err != nil {
		return nil, err
	}
	if setParams, err = rpc.WithAria(setParams, id); err != nil {
		return nil, err
	}
	owner := authz.WithSubject(inProcess(context.Background()), jkauthz.Subject{Principal: h.angelus.owner()})
	return h.ariaMethod(rpc.MethodSet)(owner, setParams)
}

func (h *handlers) refuseRerouteBy(ctx context.Context, id string) error {
	if _, boxed := callerBox(ctx); boxed {
		return fmt.Errorf("reroute: a box never moves a route: it is owner-held")
	}
	if authz.FigaroOf(ctx) == "" {
		if h.callerIsOwner(ctx) {
			return nil
		}
		return fmt.Errorf("reroute: only the owner, or a figaro holding whole set on %s, moves its route", id)
	}
	return h.figaroGrant(ctx, rpc.VerbSet, authz.Aria(id), func(v authz.FigaroView, s jkauthz.Subject) bool {
		return v.AllowWhole(s, rpc.VerbSet, authz.Aria(id))
	}, "holds only keyed set there, and a reroute writes the owner-held route")
}

func (h *handlers) dressRoute(outfits []string, pins *rpc.FormPatch) (form.Patch, error) {
	folded, err := h.dress(outfits, nil)
	if err != nil {
		return form.Patch{}, err
	}
	set, drops := routeOf(form.Snapshot{}.Apply(folded))
	if _, ok := set["system.provider"]; !ok {
		return form.Patch{}, fmt.Errorf("reroute: outfit %s names no system.provider", strings.Join(outfits, ","))
	}
	route := form.Build(form.Snapshot{}, set, drops)
	if pins == nil {
		return route, nil
	}
	pinned := map[string]json.RawMessage{}
	for _, e := range pins.Entries() {
		if !form.IsRouteKey(e.Key) {
			return form.Patch{}, fmt.Errorf("reroute: %s is not a route key", e.Key)
		}
		if !e.IsRemoval() {
			pinned[e.Key] = e.New
		}
	}
	route = form.Merge(route, *pins)
	seat, alone := seatProviderAlone(pinned)
	if !alone {
		return route, nil
	}
	under := ""
	_ = json.Unmarshal(set["system.provider"], &under)
	drop, err := orphanedModel(seat, under)
	if err != nil {
		return form.Patch{}, fmt.Errorf("reroute: %w", err)
	}
	if drop {
		route = form.Merge(route, form.Build(form.Snapshot{}, nil, []string{"system.model"}))
	}
	return route, nil
}
