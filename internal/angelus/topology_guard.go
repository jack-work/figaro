package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

func (a *Angelus) DefaultTopologyGuard() authz.TopologyGuard {
	return authz.TopologyGuard{
		Policies:  func() *authz.Policies { return a.Policies },
		Owner:     a.owner,
		Placement: a.placement,
		Holds: func(ctx context.Context, root string, taken []string) (string, string) {
			roles, ready := a.roles()
			if !ready {
				return root, "the role index is not ready"
			}
			return a.heldIn(taken, root, a.Registry.BoundPIDsByFigaro(), roles, a.keepalive(), sweepTime(ctx))
		},
		Reaped: func(ctx context.Context, root string) ([]string, bool) {
			for _, g := range a.reapedGroups() {
				if g.root == root {
					return g.nodes, a.pastGrace(g, sweepTime(ctx))
				}
			}
			return nil, false
		},
		Empty:        a.ownsNoMessages,
		MintPlaces:   a.mintPlaces,
		FanoutLimit:  a.fanoutLimit,
		LiveChildren: a.liveChildCount,
		Live:         a.isLiveAria,
		SwapLimit:    a.swapLimit,
		Quota: func(p hierarchy.Plan) error {
			if len(p.Moves) == 0 {
				return nil
			}
			return a.Policies.Shadow(authz.Quota, a.fanoutCounts().fitsMoves(p.Moves))
		},
		Committed: a.topologyCommitted,
	}
}

func (a *Angelus) fanoutLimit(id string) (int, bool, error) {
	if a.Backend != nil {
		if n, ok := a.Backend.NodeLink(id); !ok || n.Kind == nullKind || n.Kind == outfitKind {
			return 0, false, nil
		}
	}
	return a.fanoutLimitHeld(id, id)
}

func (a *Angelus) fanoutLimitHeld(ownOf string, holders ...string) (int, bool, error) {
	if a.Backend == nil {
		return 0, false, nil
	}
	snap, err := a.Backend.FormState(ownOf)
	if err != nil {
		return 0, false, err
	}
	best, bestOK, err := form.FanoutMaxChildren(snap)
	if err != nil {
		return 0, false, err
	}
	roles, _ := a.roles()
	for _, holder := range holders {
		for _, roleID := range roles.holding(holder) {
			rsnap, rerr := a.Backend.FormState(roleID)
			if rerr != nil || routingAlias(rsnap) {
				continue
			}
			n, ok, nerr := form.FanoutMaxChildren(rsnap)
			if nerr != nil || !ok {
				continue
			}
			if !bestOK || n < best {
				best, bestOK = n, true
			}
		}
	}
	return best, bestOK, nil
}

func (a *Angelus) mayMint(id string) (bool, error) {
	if a.Backend == nil {
		return false, nil
	}
	snap, err := a.Backend.FormState(id)
	if err != nil {
		return false, err
	}
	if ok, err := form.FanoutMint(snap); ok || err != nil {
		return ok, err
	}
	roles, _ := a.roles()
	for _, roleID := range roles.holding(id) {
		rsnap, rerr := a.Backend.FormState(roleID)
		if rerr != nil {
			continue
		}
		if ok, _ := form.FanoutMint(rsnap); ok {
			return true, nil
		}
	}
	return false, nil
}

func (a *Angelus) forkDescends(id, ancestor string) bool {
	if id == ancestor {
		return false
	}
	chain, err := a.chainOf(id)
	if err != nil {
		return false
	}
	for _, l := range chain {
		if l.Node == ancestor {
			return true
		}
	}
	return false
}

func (a *Angelus) swapLimit(pred, succ string) (int, bool, bool) {
	if !a.forkDescends(succ, pred) {
		return 0, false, false
	}
	max, ok, err := a.fanoutLimitHeld(pred, pred, succ)
	if err != nil {
		return 0, true, true
	}
	return max, ok, true
}

func (a *Angelus) topologyCommitted(p hierarchy.Plan) {
	a.settleFanout(p)
	a.moveSwappedLimit(p)
}

func (a *Angelus) moveSwappedLimit(p hierarchy.Plan) {
	if p.Op != hierarchy.Demote && p.Op != hierarchy.Seat || a.Backend == nil {
		return
	}
	for _, m := range p.Moves {
		if m.Node != p.Node || m.Tree != hierarchy.Drawn || !a.forkDescends(m.To, p.Node) {
			continue
		}
		if err := a.swapFanoutKeys(p.Node, m.To); err != nil {
			slog.Error("fanout: the swap moved the aria but not its limit", "pred", p.Node, "succ", m.To, "err", err)
		}
		return
	}
}

func (a *Angelus) moveQuota(pred, succ string) error {
	defer a.fanoutCounts().holdWrites(pred, succ)()
	ps, err := a.Backend.FormState(pred)
	if err != nil {
		return err
	}
	q, ok := ps.Get(form.FanoutQuotaKey)
	if !ok {
		return nil
	}
	ss, err := a.Backend.FormState(succ)
	if err != nil {
		return err
	}
	if _, err := a.Backend.ApplyForm(succ, form.Build(ss, map[string]json.RawMessage{form.FanoutQuotaKey: q}, nil)); err != nil {
		return err
	}
	if _, err = a.Backend.ApplyForm(pred, form.Build(ps, nil, []string{form.FanoutQuotaKey})); err != nil {
		return err
	}
	return a.raiseQuotaToUse(succ)
}

func (a *Angelus) raiseQuotaToUse(id string) error {
	snap, err := a.Backend.FormState(id)
	if err != nil {
		return err
	}
	quota, ok, err := form.FanoutQuota(snap)
	if err != nil || !ok {
		return err
	}
	used := a.fanoutCounts().budget(id).used
	if used <= quota {
		return nil
	}
	raw, _ := json.Marshal(used)
	_, err = a.Backend.ApplyForm(id, form.Build(snap, map[string]json.RawMessage{form.FanoutQuotaKey: raw}, nil))
	return err
}

func (a *Angelus) swapFanoutKeys(pred, succ string) error {
	defer a.fanoutCounts().holdWrites(pred, succ)()
	ps, err := a.Backend.FormState(pred)
	if err != nil {
		return err
	}
	ss, err := a.Backend.FormState(succ)
	if err != nil {
		return err
	}
	succSet, predSet := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	var succDrop, predDrop []string
	if pv, pok := ps.Get(form.FanoutQuotaKey); pok {
		succSet[form.FanoutQuotaKey] = pv
		predDrop = append(predDrop, form.FanoutQuotaKey)
	} else if _, sok := ss.Get(form.FanoutQuotaKey); sok {
		succDrop = append(succDrop, form.FanoutQuotaKey)
	}
	for _, key := range []string{form.FanoutMaxChildrenKey, form.FanoutMintKey} {
		pv, pok := ps.Get(key)
		sv, sok := ss.Get(key)
		if pok == sok && bytes.Equal(pv, sv) {
			continue
		}
		if pok {
			succSet[key] = pv
		} else {
			succDrop = append(succDrop, key)
		}
		if sok {
			predSet[key] = sv
		} else {
			predDrop = append(predDrop, key)
		}
	}
	if len(succSet)+len(succDrop) == 0 {
		return nil
	}
	if _, err := a.Backend.ApplyForm(succ, form.Build(ss, succSet, succDrop)); err != nil {
		return err
	}
	_, err = a.Backend.ApplyForm(pred, form.Build(ps, predSet, predDrop))
	return err
}

func (a *Angelus) isLiveAria(id string) bool {
	if a.Backend == nil {
		return true
	}
	meta, _ := a.Backend.Meta(id)
	return meta == nil || (meta.RetiredAtMS == 0 && meta.ReapedAtMS == 0)
}

func (a *Angelus) ownsNoMessages(id string) bool {
	n, ok := a.Backend.Node(id)
	if !ok {
		return false
	}
	log, err := a.Backend.OpenFigIR(id)
	if err != nil {
		return false
	}
	for _, en := range log.Read() {
		if en.LT > n.BranchedLT && !message.IsCeremonial(en.Payload) {
			return false
		}
	}
	return true
}

func (a *Angelus) isReapedRoot(id string) bool {
	for _, g := range a.reapedGroups() {
		if g.root == id {
			return true
		}
	}
	return false
}

func (a *Angelus) placement(id string) (string, int64) {
	zone := a.sandboxOf(id)
	keeper, ok := a.Backend.(ttlLister)
	if !ok {
		return zone, 0
	}
	for _, e := range keeper.TTLEntries() {
		if e.ID == id && e.TTL > 0 {
			return zone, e.DeadlineMS
		}
	}
	return zone, 0
}

func (a *Angelus) topology() *hierarchy.Hierarchy {
	a.topologyOnce.Do(func() {
		if a.Topology != nil {
			return
		}
		if a.Backend == nil {
			a.Topology = hierarchy.New(nil, nil)
			return
		}
		a.Topology = hierarchy.New(a.Backend.HierarchyEngine(), a.DefaultTopologyGuard())
	})
	return a.Topology
}

func (a *Angelus) topologyPrincipal(ctx context.Context) (hierarchy.Principal, error) {
	by, err := authz.TopologyPrincipal(ctx)
	if err != nil {
		return by, err
	}
	if node, via := peer.Via(ctx); via && a.Peers != nil {
		by = throughNode(by, a.Peers, node)
	}
	return by, nil
}

func throughNode(by hierarchy.Principal, members peer.Membership, node string) hierarchy.Principal {
	c, ok := by.Subject.(authz.TopologyCaller)
	if !ok {
		return by
	}
	again, held := c.Readmit, c.Subject
	c.Readmit = func(ctx context.Context) (jkauthz.Subject, error) {
		if _, ok := members.Member(node); !ok {
			return jkauthz.Subject{}, fmt.Errorf("it came through node %s, which is no longer a member", node)
		}
		if again == nil {
			return held, nil
		}
		return again(ctx)
	}
	by.Subject = c
	return by
}

func deleteSetChanged(err error) error {
	if (errors.Is(err, hierarchy.ErrPlanMismatch) || errors.Is(err, authz.ErrSplitsReapedGroup)) && !errors.Is(err, store.ErrDeleteSetChanged) {
		return fmt.Errorf("%w: %w", store.ErrDeleteSetChanged, err)
	}
	return err
}

type sweepKey struct{}

func sweepingAt(ctx context.Context, now time.Time) context.Context {
	return context.WithValue(ctx, sweepKey{}, now)
}

func sweepTime(ctx context.Context) time.Time {
	if t, ok := ctx.Value(sweepKey{}).(time.Time); ok {
		return t
	}
	return time.Now()
}

func (a *Angelus) isReaped(id string) bool {
	if a.Backend == nil {
		return false
	}
	meta, _ := a.Backend.Meta(id)
	return meta != nil && meta.ReapedAtMS != 0
}

func routingAlias(role form.Snapshot) bool {
	v := role.Lookup("lifecycle.status")
	return v != nil && *v == "routing-alias"
}
