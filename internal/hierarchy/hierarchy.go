package hierarchy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
)

type Tree int

const (
	Drawn Tree = iota + 1
	Storage
)

func (t Tree) String() string {
	switch t {
	case Drawn:
		return "drawn"
	case Storage:
		return "storage"
	}
	return "tree?"
}

type Op int

const (
	Promote Op = iota + 1
	Demote
	Arrange
	Place
	Restore
	Kill
	Sweep
	Purge
	Discard
)

func (o Op) String() string {
	switch o {
	case Promote:
		return "promote"
	case Demote:
		return "demote"
	case Arrange:
		return "arrange"
	case Place:
		return "place"
	case Restore:
		return "restore"
	case Kill:
		return "kill"
	case Sweep:
		return "sweep"
	case Purge:
		return "purge"
	case Discard:
		return "discard"
	}
	return "op?"
}

func (o Op) Deletes() bool { return o == Kill || o == Sweep || o == Purge || o == Discard }

type Principal struct {
	Name    string
	System  bool
	Subject any
}

var System = Principal{System: true}

type Move struct {
	Node, From, To string
	Tree           Tree
	NodeOwner      string
	ToOwner        string
	Rehome         bool
	WithinLineage  bool
}

type Version struct {
	Epoch  string
	Stored uint64
	Drawn  uint64
}

func (v Version) IsZero() bool { return v == Version{} }

type Plan struct {
	Op        Op
	Node      string
	Levels    int
	To        string
	Parents   map[string]string
	Restore   []string
	Revive    []string
	Storage   bool
	Recursive bool
	IfVersion Version

	Moves  []Move
	Delete []string

	Version Version
}

type Verdict struct {
	Park    string
	Accepts []string
}

type Guard interface {
	Check(ctx context.Context, by Principal, p Plan) (Verdict, error)
}

type Node struct {
	ID         string
	Aria       bool
	Stump      string
	BranchedLT uint64
	Owner      string
}

type Engine interface {
	Version() Version
	Node(id string) (Node, bool)
	Drawn(id string) (string, bool)
	Stored(id string) (string, bool)
	DrawnChildren(id string) []string
	Overrides() map[string]string
	DeleteSet(id string) []string
	StorageOrphans(id string) []string
	Placement(id string) (zone string, deadlineMS int64)
	WriteDrawn(set map[string]string, remove []string) error
	MoveStorage(id, under string) error
	Remove(id string, recursive bool, expect []string, pins map[string]string) error
}

var (
	ErrNoGuard        = errors.New("hierarchy: no guard: every plan is refused")
	ErrStale          = errors.New("hierarchy: the topology moved since the plan was made")
	ErrPlanMismatch   = errors.New("hierarchy: the plan's moves are not what the operation does")
	ErrDrawnOnly      = errors.New("hierarchy: this move may change only the drawn tree")
	ErrStorageRefused = errors.New("hierarchy: the store refused the storage move")
	ErrAtTop          = errors.New("hierarchy: nothing above to promote into")
	ErrHasBranches    = errors.New("aria has live branches")
	ErrUnsupported    = errors.New("hierarchy: this store keeps no presentation hierarchy")
	ErrParked         = errors.New("hierarchy: the plan waits on its receiving parent")
)

type Hierarchy struct {
	mu    sync.Mutex
	eng   Engine
	guard Guard
}

func New(e Engine, g Guard) *Hierarchy { return &Hierarchy{eng: e, guard: g} }

func (h *Hierarchy) Version() Version {
	if h == nil || h.eng == nil {
		return Version{}
	}
	return h.eng.Version()
}

func (h *Hierarchy) Check(ctx context.Context, by Principal, p Plan) (Plan, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.checkLocked(ctx, by, p)
}

func (h *Hierarchy) Apply(ctx context.Context, by Principal, p Plan) (Plan, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	plan, err := h.checkLocked(ctx, by, p)
	if err != nil {
		return plan, err
	}
	return h.commit(plan)
}

func (h *Hierarchy) checkLocked(ctx context.Context, by Principal, p Plan) (Plan, error) {
	if h == nil || h.guard == nil {
		return p, ErrNoGuard
	}
	if h.eng == nil {
		return p, ErrUnsupported
	}
	now := h.eng.Version()
	if !p.IfVersion.IsZero() && p.IfVersion != now {
		return p, fmt.Errorf("%w: at %+v, not %+v", ErrStale, now, p.IfVersion)
	}
	plan, err := h.derive(p)
	if err != nil {
		return plan, err
	}
	plan.Version = now
	if handed := len(p.Moves) > 0 || p.Delete != nil; handed && !sameEffect(p, plan) {
		return plan, fmt.Errorf("%w: %s %s", ErrPlanMismatch, p.Op, p.Node)
	}
	v, err := h.guard.Check(ctx, by, plan)
	if errors.Is(err, ErrDrawnOnly) && hasStorage(plan) {
		plan = drawnOnly(plan)
		v, err = h.guard.Check(ctx, by, plan)
	}
	if err != nil {
		return plan, err
	}
	if v.Park != "" {
		return plan, fmt.Errorf("%w %s", ErrParked, v.Park)
	}
	return plan, nil
}

func hasStorage(p Plan) bool {
	return slices.ContainsFunc(p.Moves, func(m Move) bool { return m.Tree == Storage })
}

func drawnOnly(p Plan) Plan {
	moves := slices.Clone(p.Moves)
	for i := range moves {
		moves[i].Tree = Drawn
	}
	p.Moves = moves
	return p
}

func sameEffect(handed, derived Plan) bool {
	if handed.Delete != nil && !sameSet(handed.Delete, derived.Delete) {
		return false
	}
	if len(handed.Moves) == 0 {
		return true
	}
	key := func(m Move) [3]string { return [3]string{m.Node, m.From, m.To} }
	a := make(map[[3]string]int, len(handed.Moves))
	for _, m := range handed.Moves {
		a[key(m)]++
	}
	for _, m := range derived.Moves {
		if a[key(m)] == 0 {
			return false
		}
		a[key(m)]--
	}
	for _, n := range a {
		if n != 0 {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		if seen[s] == 0 {
			return false
		}
		seen[s]--
	}
	return true
}

type overlay struct {
	eng   Engine
	moved map[string]string
	from  map[string]string
	order []string
}

func newOverlay(e Engine) *overlay {
	return &overlay{eng: e, moved: map[string]string{}, from: map[string]string{}}
}

func (o *overlay) parent(id string) (string, bool) {
	if p, ok := o.moved[id]; ok {
		return p, true
	}
	return o.eng.Drawn(id)
}

func (o *overlay) set(id, to string) {
	if _, ok := o.from[id]; !ok {
		was, _ := o.eng.Drawn(id)
		o.from[id] = was
		o.order = append(o.order, id)
	}
	o.moved[id] = to
}

func (o *overlay) moves(tree map[string]Tree) []Move {
	var out []Move
	for _, id := range o.order {
		to := o.moved[id]
		t := tree[id]
		if t == 0 {
			t = Drawn
		}
		if to == o.from[id] && t == Drawn {
			continue
		}
		out = append(out, Move{Node: id, From: o.from[id], To: to, Tree: t})
	}
	return out
}

func (h *Hierarchy) derive(p Plan) (Plan, error) {
	out := Plan{Op: p.Op, Node: p.Node, Levels: p.Levels, To: p.To, Parents: p.Parents, Restore: p.Restore,
		Revive: p.Revive, Storage: p.Storage, Recursive: p.Recursive, IfVersion: p.IfVersion}
	var err error
	switch p.Op {
	case Promote:
		out.Moves, out.Levels, err = h.derivePromote(p.Node, p.Levels)
	case Demote:
		out.Moves, out.To, err = h.deriveDemote(p.Node, p.To)
	case Arrange:
		out.Moves, err = h.deriveArrange(p.Parents, p.Restore, p.Storage)
	case Place:
		out.Moves, err = h.derivePlace(p.Node, p.To)
	case Restore:
		for _, id := range p.Revive {
			if _, ok := h.eng.Node(id); !ok {
				return out, fmt.Errorf("restore: no node %q", id)
			}
		}
	case Kill, Sweep, Purge, Discard:
		out.Delete, out.Moves, err = h.deriveDelete(p.Node, p.Recursive || p.Op != Kill && p.Op != Discard)
		if err == nil && p.Op == Discard && len(out.Delete) != 1 {
			err = fmt.Errorf("%w: %q has %d", ErrHasBranches, p.Node, len(out.Delete)-1)
		}
	default:
		return out, fmt.Errorf("hierarchy: unknown op %d", p.Op)
	}
	if err != nil {
		return out, err
	}
	h.annotate(out.Moves)
	return out, nil
}

func (h *Hierarchy) annotate(moves []Move) {
	for i := range moves {
		m := &moves[i]
		if n, ok := h.eng.Node(m.Node); ok {
			m.NodeOwner = n.Owner
		}
		if n, ok := h.eng.Node(m.To); ok {
			m.ToOwner = n.Owner
		}
		m.WithinLineage = h.storedBelow(m.To, m.Node)
	}
}

func (h *Hierarchy) storedBelow(to, node string) bool {
	from, ok := h.eng.Stored(node)
	if !ok || from == "" || to == "" {
		return false
	}
	seen := map[string]bool{}
	for at := to; at != "" && !seen[at]; at, _ = h.eng.Stored(at) {
		if at == node {
			return false
		}
		if at == from {
			return true
		}
		seen[at] = true
	}
	return false
}

func (h *Hierarchy) isAria(id string) bool {
	n, ok := h.eng.Node(id)
	return ok && n.Aria
}

func (h *Hierarchy) derivePromote(id string, levels int) ([]Move, int, error) {
	if _, ok := h.eng.Node(id); !ok {
		return nil, 0, fmt.Errorf("promote: no aria %q", id)
	}
	if levels <= 0 {
		return nil, 0, nil
	}
	o := newOverlay(h.eng)
	climbed := 0
	for ; climbed < levels; climbed++ {
		parent, ok := o.parent(id)
		if !ok || parent == "" || !h.isAria(parent) {
			break
		}
		grand, _ := o.parent(parent)
		if grand == id {
			break
		}
		o.set(id, grand)
		o.set(parent, id)
	}
	if climbed == 0 {
		return nil, 0, ErrAtTop
	}
	return o.moves(nil), climbed, nil
}

func (h *Hierarchy) deriveDemote(id, prefer string) ([]Move, string, error) {
	if _, ok := h.eng.Node(id); !ok {
		return nil, "", fmt.Errorf("demote: no aria %q", id)
	}
	under := h.demoteTarget(id, prefer)
	if under == "" {
		return nil, "", nil
	}
	parent, ok := h.eng.Drawn(id)
	if !ok || parent == "" {
		return nil, "", fmt.Errorf("demote: %q is a root", id)
	}
	o := newOverlay(h.eng)
	for _, kid := range h.eng.DrawnChildren(id) {
		if kid != under {
			o.set(kid, under)
		}
	}
	o.set(under, parent)
	o.set(id, under)
	return o.moves(nil), under, nil
}

func (h *Hierarchy) demoteTarget(id, prefer string) string {
	var kids []Node
	for _, k := range h.eng.DrawnChildren(id) {
		n, ok := h.eng.Node(k)
		if !ok || !n.Aria {
			continue
		}
		if k == prefer {
			return k
		}
		kids = append(kids, n)
	}
	if len(kids) == 0 {
		return ""
	}
	sort.Slice(kids, func(i, j int) bool {
		if kids[i].BranchedLT != kids[j].BranchedLT {
			return kids[i].BranchedLT < kids[j].BranchedLT
		}
		return kids[i].ID < kids[j].ID
	})
	return kids[0].ID
}

func (h *Hierarchy) stumpOf(id string) string {
	seen := map[string]bool{}
	for !seen[id] {
		seen[id] = true
		n, ok := h.eng.Node(id)
		if !ok || !n.Aria {
			return id
		}
		id, _ = h.eng.Stored(id)
	}
	return ""
}

func (h *Hierarchy) deriveArrange(parents map[string]string, restore []string, storage bool) ([]Move, error) {
	for _, id := range restore {
		if !h.isAria(id) {
			return nil, fmt.Errorf("arrange: no aria %q", id)
		}
	}
	ids := make([]string, 0, len(parents))
	for id := range parents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	o := newOverlay(h.eng)
	trees := map[string]Tree{}
	over := h.eng.Overrides()
	for _, id := range restore {
		up, _ := h.eng.Stored(id)
		o.set(id, up)
		delete(over, id)
	}
	for _, id := range ids {
		if !h.isAria(id) {
			return nil, fmt.Errorf("arrange: no aria %q", id)
		}
		parent := parents[id]
		explicit := parent != ""
		if !explicit {
			parent = h.stumpOf(id)
		} else if parent == id || !h.isAria(parent) {
			return nil, fmt.Errorf("arrange: %q cannot be drawn under %q: not another aria", id, parent)
		}
		o.set(id, parent)
		over[id] = parent
		if storage && explicit {
			if up, _ := h.eng.Stored(id); up != parent {
				trees[id] = Storage
			}
		}
	}
	for _, id := range ids {
		if under, ok := drawnBelow(o.parent, id); ok {
			return nil, fmt.Errorf("trunk: %q cannot be drawn under %q, which is drawn under it", id, under)
		}
	}
	stale := make([]string, 0, len(over))
	for id := range over {
		if _, set := parents[id]; !set {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	for _, id := range stale {
		if _, ok := drawnBelow(o.parent, id); ok {
			up, _ := h.eng.Stored(id)
			o.set(id, up)
		}
	}
	return o.moves(trees), nil
}

func drawnBelow(parentOf func(string) (string, bool), id string) (string, bool) {
	first, _ := parentOf(id)
	seen := map[string]bool{}
	for at := first; at != "" && !seen[at]; {
		if at == id {
			return first, true
		}
		seen[at] = true
		at, _ = parentOf(at)
	}
	return "", false
}

func (h *Hierarchy) derivePlace(id, under string) ([]Move, error) {
	if _, ok := h.eng.Node(id); !ok {
		return nil, fmt.Errorf("place: no node %q", id)
	}
	if _, ok := h.eng.Node(under); !ok {
		return nil, fmt.Errorf("place: no node %q", under)
	}
	if len(h.eng.DrawnChildren(id)) > 0 {
		return nil, fmt.Errorf("place: %q already has children; arrange it instead", id)
	}
	if _, drawn := h.eng.Overrides()[id]; drawn {
		return nil, fmt.Errorf("place: %q is already drawn elsewhere; arrange it instead", id)
	}
	if up, _ := h.eng.Stored(id); up == under {
		return nil, nil
	}
	from, _ := h.eng.Drawn(id)
	return []Move{{Node: id, From: from, To: under, Tree: Storage}}, nil
}

func (h *Hierarchy) deriveDelete(id string, recursive bool) ([]string, []Move, error) {
	if _, ok := h.eng.Node(id); !ok {
		return nil, nil, fmt.Errorf("no aria %q", id)
	}
	taken := h.eng.DeleteSet(id)
	if len(taken) == 0 {
		taken = []string{id}
	}
	if !recursive && len(taken) > 1 {
		return nil, nil, fmt.Errorf("%w: %q has %d; -r takes them too", ErrHasBranches, id, len(taken)-1)
	}
	doomed := make(map[string]bool, len(taken))
	for _, t := range taken {
		doomed[t] = true
	}
	var moves []Move
	homed := map[string]bool{}
	rehome := func(orphan string) {
		if homed[orphan] {
			return
		}
		homed[orphan] = true
		from, _ := h.eng.Drawn(orphan)
		to := from
		if doomed[from] || !h.fits(orphan, from) {
			to = h.home(orphan, doomed)
		}
		moves = append(moves, Move{Node: orphan, From: from, To: to, Tree: Drawn, Rehome: true})
	}
	for child, up := range h.eng.Overrides() {
		if !doomed[child] && doomed[up] {
			rehome(child)
		}
	}
	for _, orphan := range h.eng.StorageOrphans(id) {
		rehome(orphan)
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].Node < moves[j].Node })
	return taken, moves, nil
}

func (h *Hierarchy) home(id string, doomed map[string]bool) string {
	seen := map[string]bool{id: true}
	for up, ok := h.eng.Stored(id); ok && up != "" && !seen[up]; up, ok = h.eng.Stored(up) {
		seen[up] = true
		if doomed[up] || !h.isAria(up) {
			continue
		}
		if h.fits(id, up) {
			return up
		}
	}
	if n, ok := h.eng.Node(id); ok && n.Stump != "" && !doomed[n.Stump] {
		return n.Stump
	}
	return ""
}

func (h *Hierarchy) fits(id, under string) bool {
	if under == "" {
		return true
	}
	if n, ok := h.eng.Node(under); !ok || !n.Aria {
		return true
	}
	return Fits(h.eng.Placement, id, under)
}

func Fits(placement func(string) (string, int64), id, under string) bool {
	zone, deadline := placement(id)
	uz, ud := placement(under)
	if uz != zone {
		return false
	}
	return ud == 0 || deadline != 0 && ud >= deadline
}

func (h *Hierarchy) commit(p Plan) (Plan, error) {
	if p.Op == Restore {
		return p, nil
	}
	if p.Op.Deletes() {
		pins := map[string]string{}
		for _, m := range p.Moves {
			pins[m.Node] = m.To
		}
		return p, h.eng.Remove(p.Node, p.Recursive || p.Op == Sweep || p.Op == Purge, p.Delete, pins)
	}
	for i := range p.Moves {
		m := &p.Moves[i]
		if m.Tree != Storage {
			continue
		}
		err := h.eng.MoveStorage(m.Node, m.To)
		if errors.Is(err, ErrStorageRefused) {
			m.Tree = Drawn
			continue
		}
		if err != nil {
			return p, err
		}
	}
	set := map[string]string{}
	var remove []string
	for _, m := range p.Moves {
		up, _ := h.eng.Stored(m.Node)
		if m.To == up {
			remove = append(remove, m.Node)
			continue
		}
		set[m.Node] = m.To
	}
	if len(set) == 0 && len(remove) == 0 {
		return p, nil
	}
	return p, h.eng.WriteDrawn(set, remove)
}
