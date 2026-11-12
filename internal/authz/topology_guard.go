package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/jkauthz"
)

type TopologyCaller struct {
	Subject jkauthz.Subject
	Figaro  string
	Door    Door
	Until   time.Time
	Readmit func(context.Context) (jkauthz.Subject, error)
}

func TopologyPrincipal(ctx context.Context) (hierarchy.Principal, error) {
	sub, err := SubjectOf(ctx)
	if err != nil {
		return hierarchy.Principal{}, err
	}
	c := TopologyCaller{Subject: sub, Figaro: FigaroOf(ctx), Door: DoorOf(ctx)}
	c.Until, c.Readmit = admission(ctx)
	return hierarchy.Principal{Name: PrincipalText(PrincipalOf(ctx)), Subject: c}, nil
}

type admittedUntilKey struct{}

func WithAdmittedUntil(ctx context.Context, t time.Time) context.Context {
	if held, ok := ctx.Value(admittedUntilKey{}).(time.Time); ok && held.Before(t) {
		return ctx
	}
	return context.WithValue(ctx, admittedUntilKey{}, t)
}

func admission(ctx context.Context) (time.Time, func(context.Context) (jkauthz.Subject, error)) {
	until, _ := ctx.Value(admittedUntilKey{}).(time.Time)
	c, _ := ctx.Value(callerKey{}).(caller)
	switch {
	case c.session != nil:
		return earliest(until, c.session.Expiry()), c.session.Subject
	case c.key != nil:
		k := c.key
		return earliest(until, k.expiry()), func(context.Context) (jkauthz.Subject, error) { return k.Subject() }
	}
	return until, nil
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

type HeldError struct {
	Node, Why string
}

func (e *HeldError) Error() string { return fmt.Sprintf("%s holds the sweep: %s", e.Node, e.Why) }

var ErrSystemOnly = errors.New("only the daemon may run this plan")

var ErrSplitsReapedGroup = errors.New("a delete would split a reaped group")

type TopologyGuard struct {
	Policies   func() *Policies
	Owner      func() jkauthz.Principal
	Placement  func(id string) (zone string, deadlineMS int64)
	Holds      func(ctx context.Context, root string, taken []string) (node, why string)
	Reaped     func(ctx context.Context, root string) (nodes []string, due bool)
	Empty      func(id string) bool
	MintPlaces func(box, node, under string) error
}

func (g TopologyGuard) Check(ctx context.Context, by hierarchy.Principal, p hierarchy.Plan) (hierarchy.Verdict, error) {
	if err := g.rehomes(p); err != nil {
		return hierarchy.Verdict{}, err
	}
	if err := g.wholeGroups(ctx, p); err != nil {
		return hierarchy.Verdict{}, err
	}
	if by.System {
		return hierarchy.Verdict{}, g.system(ctx, p)
	}
	switch p.Op {
	case hierarchy.Sweep, hierarchy.Purge, hierarchy.Discard, hierarchy.Depart:
		return hierarchy.Verdict{}, fmt.Errorf("%s: %w", p.Op, ErrSystemOnly)
	}
	caller, ok := by.Subject.(TopologyCaller)
	if !ok {
		return hierarchy.Verdict{}, fmt.Errorf("%s: no caller to authorize", p.Op)
	}
	if IsProvisioner(caller.Subject.Principal) && !p.Op.Deletes() && p.Op != hierarchy.Restore {
		return hierarchy.Verdict{}, g.provisionerPlaces(caller, p)
	}
	switch p.Op {
	case hierarchy.Kill, hierarchy.Reap:
		if err := g.each(caller, rpc.VerbKill, p.Delete); err != nil || !p.Lift {
			return hierarchy.Verdict{}, err
		}
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
	if err := g.storage(caller, p); err != nil {
		return hierarchy.Verdict{}, err
	}
	return g.consent(ctx, by, caller, p)
}

func (g TopologyGuard) MayConsent(_ context.Context, by hierarchy.Principal, parent string) error {
	if by.System {
		return fmt.Errorf("adopt on %s: the daemon consents to nothing", Aria(parent))
	}
	c, ok := by.Subject.(TopologyCaller)
	if !ok {
		return fmt.Errorf("adopt on %s: no caller to authorize", Aria(parent))
	}
	return g.allow(c, rpc.VerbAdopt, parent)
}

func (g TopologyGuard) Readmit(ctx context.Context, by hierarchy.Principal) (hierarchy.Principal, error) {
	if by.System {
		return by, nil
	}
	c, ok := by.Subject.(TopologyCaller)
	if !ok {
		return by, fmt.Errorf("%s: no caller to admit", by.Name)
	}
	if c.Readmit == nil {
		return by, nil
	}
	sub, err := c.Readmit(ctx)
	if err != nil {
		return by, err
	}
	if sub.Principal != c.Subject.Principal {
		return by, fmt.Errorf("%w: proposed as %s, now %s", ErrUnadmitted, c.Subject.Principal, sub.Principal)
	}
	c.Subject = sub
	by.Subject = c
	return by, nil
}

func (g TopologyGuard) consent(ctx context.Context, by hierarchy.Principal, c TopologyCaller, p hierarchy.Plan) (hierarchy.Verdict, error) {
	var waits []string
	var accepts []string
	for _, m := range p.Moves {
		if m.Rehome || !m.ToReceiver || m.Home || m.From == m.To && m.Tree == hierarchy.Drawn {
			continue
		}
		if g.ownsParent(by, c, m.ToOwner) {
			continue
		}
		if p.Consent != nil && p.Consent.Parent == m.To {
			if err := g.MayConsent(ctx, p.Consent.By, m.To); err != nil {
				return hierarchy.Verdict{}, err
			}
			continue
		}
		if g.allow(c, rpc.VerbAdopt, m.To) == nil || slices.Contains(waits, m.To) {
			continue
		}
		waits = append(waits, m.To)
		accepts = append(accepts, g.ownerText(m.ToOwner), m.To)
	}
	switch len(waits) {
	case 0:
		return hierarchy.Verdict{}, nil
	case 1:
		v := hierarchy.Verdict{Park: waits[0], Accepts: accepts}
		if !g.isOwner(c) {
			v.Until = c.Until
		}
		return v, nil
	}
	return hierarchy.Verdict{}, fmt.Errorf("%s: %w: %v", p.Op, hierarchy.ErrManyParents, waits)
}

func (g TopologyGuard) ownerText(minter string) string {
	if minter == "" {
		return g.owner().String()
	}
	return minter
}

func (g TopologyGuard) ownsParent(by hierarchy.Principal, c TopologyCaller, minter string) bool {
	if g.ownerish(minter) {
		return g.isOwner(c)
	}
	return c.Door != DoorBox && by.Name == minter
}

func (g TopologyGuard) system(ctx context.Context, p hierarchy.Plan) error {
	switch p.Op {
	case hierarchy.Sweep, hierarchy.Reap:
		if err := g.sweepable(ctx, p); err != nil {
			return err
		}
	case hierarchy.Purge:
		if err := g.purgeable(ctx, p); err != nil {
			return err
		}
	case hierarchy.Discard:
		if len(p.Delete) != 1 || p.Delete[0] != p.Node || len(p.Moves) > 0 {
			return fmt.Errorf("discard takes one leaf, not %v", p.Delete)
		}
		if g.Empty == nil || !g.Empty(p.Node) {
			return fmt.Errorf("discard: %s holds messages of its own", p.Node)
		}
	case hierarchy.Depart:
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

func (g TopologyGuard) sweepable(ctx context.Context, p hierarchy.Plan) error {
	root := p.Node
	zone := g.zone(root)
	for _, id := range p.Delete {
		if id != root && g.zone(id) != zone {
			return &HeldError{Node: id, Why: "a node it would take runs in another sandbox"}
		}
	}
	if g.Holds == nil {
		return &HeldError{Node: root, Why: "no hold rule is wired"}
	}
	if node, why := g.Holds(ctx, root, p.Delete); why != "" {
		return &HeldError{Node: node, Why: why}
	}
	return nil
}

func (g TopologyGuard) purgeable(ctx context.Context, p hierarchy.Plan) error {
	if g.Reaped == nil {
		return fmt.Errorf("purge %s: no reaped rule is wired", p.Node)
	}
	nodes, due := g.Reaped(ctx, p.Node)
	if !due {
		return fmt.Errorf("purge %s: not a reaped root past its grace", p.Node)
	}
	reaped := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		reaped[n] = true
	}
	for _, id := range p.Delete {
		if !reaped[id] {
			return fmt.Errorf("purge %s: %s was not reaped with it", p.Node, id)
		}
	}
	return nil
}

func (g TopologyGuard) wholeGroups(ctx context.Context, p hierarchy.Plan) error {
	switch p.Op {
	case hierarchy.Kill, hierarchy.Sweep, hierarchy.Purge, hierarchy.Discard:
	default:
		return nil
	}
	if g.Reaped == nil {
		return nil
	}
	taken := make(map[string]bool, len(p.Delete))
	for _, id := range p.Delete {
		taken[id] = true
	}
	for _, id := range p.Delete {
		nodes, _ := g.Reaped(ctx, id)
		for _, n := range nodes {
			if !taken[n] {
				return fmt.Errorf("%w: %s was reaped with %s, and this %s would leave it behind", ErrSplitsReapedGroup, n, id, p.Op)
			}
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

func (g TopologyGuard) provisionerPlaces(c TopologyCaller, p hierarchy.Plan) error {
	if p.Op != hierarchy.Place {
		return fmt.Errorf("%s %s: %s moves a node only by placing one it just minted under its pool's node", p.Op, p.Node, c.Subject.Principal)
	}
	box, ok := leaseServed(c.Subject)
	if !ok || g.MintPlaces == nil {
		return fmt.Errorf("place %s under %s: %s places only while it mints for one lease", p.Node, p.To, c.Subject.Principal)
	}
	for _, m := range p.Moves {
		if m.Node != p.Node || m.To != p.To {
			return fmt.Errorf("place %s under %s: the plan also moves %s under %s", p.Node, p.To, m.Node, m.To)
		}
	}
	if err := g.MintPlaces(box, p.Node, p.To); err != nil {
		return fmt.Errorf("place %s under %s: %w", p.Node, p.To, err)
	}
	return nil
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
