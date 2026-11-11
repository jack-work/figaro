package angelus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

func (a *Angelus) DefaultTopologyGuard() authz.TopologyGuard {
	return authz.TopologyGuard{
		Policies:  func() *authz.Policies { return a.Policies },
		Owner:     a.owner,
		Placement: a.placement,
		Holds: func(ctx context.Context, root string, taken []string) (string, string) {
			roles, ready := a.roleTargets()
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
		Empty: a.ownsNoMessages,
	}
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

func topologyPrincipal(ctx context.Context) (hierarchy.Principal, error) {
	return authz.TopologyPrincipal(ctx)
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
