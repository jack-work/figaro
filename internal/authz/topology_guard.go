package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/jkauthz"
)

type TopologyCaller struct {
	Subject jkauthz.Subject
	Figaro  string
	Door    Door
}

func TopologyPrincipal(ctx context.Context) (hierarchy.Principal, error) {
	sub, err := SubjectOf(ctx)
	if err != nil {
		return hierarchy.Principal{}, err
	}
	return hierarchy.Principal{
		Name:    PrincipalText(PrincipalOf(ctx)),
		Subject: TopologyCaller{Subject: sub, Figaro: FigaroOf(ctx), Door: DoorOf(ctx)},
	}, nil
}

type HeldError struct {
	Node, Why string
}

func (e *HeldError) Error() string { return fmt.Sprintf("%s holds the sweep: %s", e.Node, e.Why) }

var ErrSystemOnly = errors.New("only the daemon may run this plan")

type TopologyGuard struct {
	Policies  func() *Policies
	Owner     func() jkauthz.Principal
	Placement func(id string) (zone string, deadlineMS int64)
	Holds     func(ctx context.Context, root string, taken []string) (node, why string)
}

func (g TopologyGuard) Check(ctx context.Context, by hierarchy.Principal, p hierarchy.Plan) (hierarchy.Verdict, error) {
	if err := g.rehomes(p); err != nil {
		return hierarchy.Verdict{}, err
	}
	if by.System {
		return hierarchy.Verdict{}, g.system(ctx, p)
	}
	switch p.Op {
	case hierarchy.Sweep, hierarchy.Purge, hierarchy.Discard:
		return hierarchy.Verdict{}, fmt.Errorf("%s: %w", p.Op, ErrSystemOnly)
	}
	caller, ok := by.Subject.(TopologyCaller)
	if !ok {
		return hierarchy.Verdict{}, fmt.Errorf("%s: no caller to authorize", p.Op)
	}
	switch p.Op {
	case hierarchy.Kill:
		return hierarchy.Verdict{}, g.each(caller, rpc.VerbKill, p.Delete)
	case hierarchy.Restore:
		return hierarchy.Verdict{}, g.each(caller, rpc.VerbKill, p.Revive)
	case hierarchy.Place:
		if g.freshPlacement(by, p) {
			return hierarchy.Verdict{}, nil
		}
	}
	if err := g.killFloor(caller, p); err != nil {
		return hierarchy.Verdict{}, err
	}
	return hierarchy.Verdict{}, g.storage(caller, p)
}

func (g TopologyGuard) system(ctx context.Context, p hierarchy.Plan) error {
	switch p.Op {
	case hierarchy.Sweep:
		root := p.Node
		zone := g.zone(root)
		for _, id := range p.Delete {
			if id != root && g.zone(id) != zone {
				return &HeldError{Node: id, Why: "a node it would take runs in another sandbox"}
			}
		}
		if g.Holds != nil {
			if node, why := g.Holds(ctx, root, p.Delete); why != "" {
				return &HeldError{Node: node, Why: why}
			}
		}
	case hierarchy.Purge, hierarchy.Discard:
	default:
		return fmt.Errorf("the daemon may only re-home and sweep, not %s", p.Op)
	}
	for _, m := range p.Moves {
		if !m.Rehome || m.Tree != hierarchy.Drawn {
			return fmt.Errorf("the daemon may only re-home in the drawn tree: %s to %s", m.Node, m.To)
		}
	}
	return nil
}

func (g TopologyGuard) rehomes(p hierarchy.Plan) error {
	for _, m := range p.Moves {
		if !m.Rehome {
			continue
		}
		if !p.Op.Deletes() || m.Tree != hierarchy.Drawn {
			return fmt.Errorf("a re-home rides only a delete, in the drawn tree: %s", m.Node)
		}
		if m.To == "" || g.Placement == nil {
			continue
		}
		if !hierarchy.Fits(g.Placement, m.Node, m.To) {
			return fmt.Errorf("%s may not be re-homed under %s: another sandbox or a nearer deadline", m.Node, m.To)
		}
	}
	return nil
}

func (g TopologyGuard) killFloor(c TopologyCaller, p hierarchy.Plan) error {
	for _, m := range p.Moves {
		if m.Rehome || m.From == m.To && m.Tree == hierarchy.Drawn {
			continue
		}
		if err := g.allow(c, rpc.VerbKill, m.Node); err != nil {
			return fmt.Errorf("%s moves %s: %w", p.Op, m.Node, err)
		}
	}
	return nil
}

func (g TopologyGuard) storage(c TopologyCaller, p hierarchy.Plan) error {
	for _, m := range p.Moves {
		if m.Tree != hierarchy.Storage {
			continue
		}
		if !g.isOwner(c) || !g.ownerish(m.NodeOwner) || !g.ownerish(m.ToOwner) {
			return fmt.Errorf("%w: %s under %s", hierarchy.ErrDrawnOnly, m.Node, m.To)
		}
	}
	return nil
}

func (g TopologyGuard) freshPlacement(by hierarchy.Principal, p hierarchy.Plan) bool {
	for _, m := range p.Moves {
		if m.Tree != hierarchy.Storage || !m.WithinLineage || m.NodeOwner != by.Name {
			return false
		}
	}
	return true
}

func (g TopologyGuard) each(c TopologyCaller, verb rpc.Verb, ids []string) error {
	for _, id := range ids {
		if err := g.allow(c, verb, id); err != nil {
			return err
		}
	}
	return nil
}

func (g TopologyGuard) owner() jkauthz.Principal {
	if g.Owner == nil {
		return jkauthz.Principal{}
	}
	return g.Owner()
}

func (g TopologyGuard) isOwner(c TopologyCaller) bool {
	return c.Door != DoorBox && IsOwner(g.owner(), c.Subject)
}

func (g TopologyGuard) ownerish(minter string) bool {
	if minter == "" {
		return true
	}
	o := g.owner()
	return !o.IsZero() && minter == o.String()
}

func (g TopologyGuard) zone(id string) string {
	if g.Placement == nil {
		return ""
	}
	z, _ := g.Placement(id)
	return z
}

func (g TopologyGuard) allow(c TopologyCaller, verb rpc.Verb, id string) error {
	var pol *Policies
	if g.Policies != nil {
		pol = g.Policies()
	}
	if pol == nil {
		if PassesUnpoliced(c.Door, g.owner(), c.Subject) {
			return nil
		}
		return fmt.Errorf("%s on %s: no policy is loaded, so the %s door passes only the owner", verb, Aria(id), c.Door)
	}
	if d := pol.AllowFigaro(c.Subject, c.Figaro, verb, Aria(id)); !d.Allowed {
		return fmt.Errorf("%s on %s: %s", verb, Aria(id), d.Reason())
	}
	return nil
}
