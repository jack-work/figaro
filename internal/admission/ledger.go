package admission

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrMaxChildren = errors.New("admission: max_children")
	ErrQuota       = errors.New("admission: fan-out quota")
	ErrUnknown     = errors.New("admission: unknown node")
	ErrHasChildren = errors.New("admission: node has children")
	ErrCycle       = errors.New("admission: a move under its own descendant")
	ErrMoving      = errors.New("admission: a move above is in flight")
)

type Limits struct {
	MaxChildren    int
	HasMaxChildren bool
	Quota          int
	HasQuota       bool
}

type Node struct {
	ID     string
	Parent string
	Live   bool
	Limits Limits
}

type Ticket uint64

type entry struct {
	parent string
	live   bool
	limits Limits
	kids   int
	used   int
	nkids  int
	ghost  *entry
}

type Ledger struct {
	mu   sync.Mutex
	at   map[string]*entry
	next Ticket
}

func New() *Ledger { return &Ledger{at: map[string]*entry{}} }

func (l *Ledger) Rebuild(nodes []Node) {
	at := make(map[string]*entry, len(nodes))
	for _, n := range nodes {
		at[n.ID] = &entry{parent: n.Parent, live: n.Live, limits: n.Limits}
	}
	for _, n := range nodes {
		if p := at[n.Parent]; p != nil {
			p.nkids++
			if n.Live {
				p.kids++
			}
		}
	}
	ready := make([]string, 0, len(nodes))
	waiting := make(map[string]int, len(nodes))
	for id, e := range at {
		waiting[id] = e.nkids
		if e.nkids == 0 {
			ready = append(ready, id)
		}
	}
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		e := at[id]
		p := at[e.parent]
		if p == nil {
			continue
		}
		p.used += chargeOf(e)
		if waiting[e.parent]--; waiting[e.parent] == 0 {
			ready = append(ready, e.parent)
		}
	}
	l.mu.Lock()
	l.at = at
	l.mu.Unlock()
}

func chargeOf(e *entry) int {
	switch {
	case !e.live:
		return e.used
	case e.limits.HasQuota:
		return 1 + e.limits.Quota
	default:
		return 1 + e.used
	}
}

func absorbs(e *entry) bool { return e.live && e.limits.HasQuota }

func (l *Ledger) fits(from string, d int) error {
	if d <= 0 {
		return nil
	}
	for id := from; id != ""; {
		e := l.at[id]
		if e == nil {
			return nil
		}
		if e.limits.HasQuota && e.used+d > e.limits.Quota {
			return fmt.Errorf("%w: %s uses %d of %d and needs %d more", ErrQuota, id, e.used, e.limits.Quota, d)
		}
		if absorbs(e) {
			return nil
		}
		if e.ghost != nil && l.quotaOver(e.ghost.parent) {
			return fmt.Errorf("%w: %s is moving between two budgets", ErrMoving, id)
		}
		id = e.parent
	}
	return nil
}

func (l *Ledger) quotaOver(from string) bool {
	seen := map[string]bool{}
	for id := from; id != "" && !seen[id]; {
		seen[id] = true
		e := l.at[id]
		if e == nil {
			return false
		}
		if e.limits.HasQuota {
			return true
		}
		id = e.parent
	}
	return false
}

func (l *Ledger) carry(from string, d int) {
	if d == 0 {
		return
	}
	for id := from; id != ""; {
		e := l.at[id]
		if e == nil {
			return
		}
		e.used += d
		if absorbs(e) {
			return
		}
		id = e.parent
	}
}

func (l *Ledger) Reserve(parent string, grant Limits) (Ticket, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.at[parent]; p != nil && p.limits.HasMaxChildren && p.kids >= p.limits.MaxChildren {
		return 0, fmt.Errorf("%w: %s holds %d live children of %d", ErrMaxChildren, parent, p.kids, p.limits.MaxChildren)
	}
	e := &entry{parent: parent, live: true, limits: grant}
	if err := l.fits(parent, chargeOf(e)); err != nil {
		return 0, err
	}
	l.next++
	t := l.next
	l.at[ticketID(t)] = e
	l.attach(e)
	return t, nil
}

func ticketID(t Ticket) string { return fmt.Sprintf("\x00ticket/%d", t) }

func (l *Ledger) attach(e *entry) {
	if p := l.at[e.parent]; p != nil {
		p.nkids++
		if e.live {
			p.kids++
		}
	}
	l.carry(e.parent, chargeOf(e))
}

func (l *Ledger) detach(e *entry) {
	if p := l.at[e.parent]; p != nil {
		p.nkids--
		if e.live {
			p.kids--
		}
	}
	l.carry(e.parent, -chargeOf(e))
}

func (l *Ledger) Commit(t Ticket, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.at[ticketID(t)]; e != nil {
		delete(l.at, ticketID(t))
		l.at[id] = e
	}
}

func (l *Ledger) Abort(t Ticket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.at[ticketID(t)]; e != nil {
		l.detach(e)
		delete(l.at, ticketID(t))
	}
}

func (l *Ledger) Add(n Node) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.at[n.ID]; ok {
		return
	}
	e := &entry{parent: n.Parent, live: n.Live, limits: n.Limits}
	l.at[n.ID] = e
	l.attach(e)
}

func (l *Ledger) SetLive(id string, live bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil {
		return fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	if e.live == live {
		return nil
	}
	next := *e
	next.live = live
	if live {
		if p := l.at[e.parent]; p != nil && p.limits.HasMaxChildren && p.kids >= p.limits.MaxChildren {
			return fmt.Errorf("%w: %s holds %d live children of %d", ErrMaxChildren, e.parent, p.kids, p.limits.MaxChildren)
		}
		if err := l.fits(e.parent, chargeOf(&next)-chargeOf(e)); err != nil {
			return err
		}
	}
	l.detach(e)
	e.live = live
	l.attach(e)
	return nil
}

func (l *Ledger) SetLimits(id string, lim Limits) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil {
		return fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	if lim.HasQuota && e.used > lim.Quota {
		return fmt.Errorf("%w: %s already uses %d, more than %d", ErrQuota, id, e.used, lim.Quota)
	}
	next := *e
	next.limits = lim
	if err := l.fits(e.parent, chargeOf(&next)-chargeOf(e)); err != nil {
		return err
	}
	l.detach(e)
	e.limits = lim
	l.attach(e)
	return nil
}

func (l *Ledger) Move(id, to string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil {
		return fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	if e.parent == to {
		return nil
	}
	if l.under(to, id) {
		return fmt.Errorf("%w: %s under %s", ErrCycle, id, to)
	}
	from := e.parent
	l.detach(e)
	e.parent = to
	err := l.admitArrival(e)
	if err != nil {
		e.parent = from
	}
	l.attach(e)
	return err
}

func (l *Ledger) admitArrival(e *entry) error {
	if p := l.at[e.parent]; e.live && p != nil && p.limits.HasMaxChildren && p.kids >= p.limits.MaxChildren {
		return fmt.Errorf("%w: %s holds %d live children of %d", ErrMaxChildren, e.parent, p.kids, p.limits.MaxChildren)
	}
	return l.fits(e.parent, chargeOf(e))
}

func (l *Ledger) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil {
		return nil
	}
	if e.nkids > 0 {
		return fmt.Errorf("%w: %s holds %d", ErrHasChildren, id, e.nkids)
	}
	l.detach(e)
	delete(l.at, id)
	return nil
}

type Count struct {
	LiveChildren int
	Used         int
	Charge       int
}

func (l *Ledger) Of(id string) (Count, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil {
		return Count{}, false
	}
	return Count{LiveChildren: e.kids, Used: e.used, Charge: chargeOf(e)}, true
}

func (l *Ledger) Mark(id string, live bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil || e.live == live {
		return
	}
	l.detach(e)
	e.live = live
	l.attach(e)
}

func (l *Ledger) Refresh(id string, lim Limits) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.at[id]
	if e == nil || e.limits == lim {
		return
	}
	l.detach(e)
	e.limits = lim
	l.attach(e)
}

type Placement struct {
	ID, Parent string
}

type Arrival struct {
	Node, To string
}

type Shift struct {
	l     *Ledger
	moves []shifted
}

type shifted struct {
	id, from string
	ghost    Ticket
}

func (l *Ledger) Admit(arrivals []Arrival) (*Shift, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	gain, loss := map[string]int{}, map[string]int{}
	var moving []Arrival
	var targets []string
	for _, a := range arrivals {
		e := l.at[a.Node]
		if e == nil || e.parent == a.To || e.ghost != nil {
			continue
		}
		if l.under(a.To, a.Node) {
			return nil, fmt.Errorf("%w: %s under %s", ErrCycle, a.Node, a.To)
		}
		moving = append(moving, a)
		if !e.live {
			continue
		}
		if gain[a.To] == 0 {
			targets = append(targets, a.To)
		}
		gain[a.To]++
		loss[e.parent]++
	}
	for _, to := range targets {
		p := l.at[to]
		if p == nil || !p.limits.HasMaxChildren {
			continue
		}
		if final := p.kids - loss[to] + gain[to]; final > p.limits.MaxChildren {
			return nil, fmt.Errorf("%w: %s would hold %d live children of %d", ErrMaxChildren, to, final, p.limits.MaxChildren)
		}
	}
	s := &Shift{l: l}
	for _, a := range moving {
		e := l.at[a.Node]
		if l.under(a.To, a.Node) {
			s.revertLocked(nil)
			return nil, fmt.Errorf("%w: %s under %s", ErrCycle, a.Node, a.To)
		}
		if err := l.fits(a.To, chargeOf(e)); err != nil {
			s.revertLocked(nil)
			return nil, err
		}
		g := &entry{parent: e.parent, live: e.live, limits: e.limits, used: e.used}
		l.next++
		l.at[ticketID(l.next)] = g
		from := e.parent
		l.detach(e)
		l.attach(g)
		e.parent = a.To
		e.ghost = g
		l.attach(e)
		s.moves = append(s.moves, shifted{id: a.Node, from: from, ghost: l.next})
	}
	return s, nil
}

func (s *Shift) revertLocked(kept map[string]bool) {
	l := s.l
	for i := len(s.moves) - 1; i >= 0; i-- {
		m := s.moves[i]
		g := l.at[ticketID(m.ghost)]
		e := l.at[m.id]
		if e != nil {
			e.ghost = nil
		}
		if e != nil && !kept[m.id] {
			l.detach(e)
			e.parent = m.from
			l.attach(e)
		}
		if g != nil {
			l.detach(g)
			delete(l.at, ticketID(m.ghost))
		}
	}
	s.moves = nil
}

func (l *Ledger) Settle(s *Shift, placed []Placement, gone []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s != nil && s.l == l {
		kept := make(map[string]bool, len(placed))
		for _, p := range placed {
			kept[p.ID] = true
		}
		s.revertLocked(kept)
	}
	for _, p := range placed {
		e := l.at[p.ID]
		if e == nil || e.parent == p.Parent || l.under(p.Parent, p.ID) {
			continue
		}
		l.detach(e)
		e.parent = p.Parent
		l.attach(e)
	}
	for _, id := range gone {
		if e := l.at[id]; e != nil {
			l.detach(e)
			delete(l.at, id)
		}
	}
}

type Drift struct {
	ID   string
	Have string
	Want string
}

func (l *Ledger) Audit(nodes []Node) []Drift {
	l.mu.Lock()
	inFlight := make([]Node, 0)
	limits := make(map[string]Limits, len(l.at))
	for id, e := range l.at {
		limits[id] = e.limits
		if isTicket(id) {
			inFlight = append(inFlight, Node{ID: id, Parent: e.parent, Live: e.live, Limits: e.limits})
		}
	}
	l.mu.Unlock()
	seen := make(map[string]bool, len(nodes))
	all := make([]Node, 0, len(nodes)+len(inFlight))
	for _, n := range nodes {
		n.Limits = limits[n.ID]
		seen[n.ID] = true
		all = append(all, n)
	}
	all = append(all, inFlight...)
	fresh := New()
	fresh.Rebuild(all)
	have, want := l.snapshot(), fresh.snapshot()
	var out []Drift
	for id, w := range want {
		h, ok := have[id]
		if !ok {
			out = append(out, Drift{ID: id, Have: "absent", Want: describe(w)})
			continue
		}
		if h.parent != w.parent || h.live != w.live || h.kids != w.kids || h.nkids != w.nkids || h.used != w.used {
			out = append(out, Drift{ID: id, Have: describe(h), Want: describe(w)})
		}
	}
	for id, h := range have {
		if _, ok := want[id]; !ok && !seen[id] {
			out = append(out, Drift{ID: id, Have: describe(h), Want: "absent"})
		}
	}
	return out
}

func isTicket(id string) bool { return len(id) > 0 && id[0] == 0 }

func describe(e entry) string {
	return fmt.Sprintf("parent=%q live=%t kids=%d nodes=%d used=%d", e.parent, e.live, e.kids, e.nkids, e.used)
}

func (l *Ledger) snapshot() map[string]entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]entry, len(l.at))
	for id, e := range l.at {
		out[id] = *e
	}
	return out
}

func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.at)
}

func (l *Ledger) under(id, ancestor string) bool {
	for up := id; up != ""; {
		if up == ancestor {
			return true
		}
		u := l.at[up]
		if u == nil {
			return false
		}
		up = u.parent
	}
	return false
}
