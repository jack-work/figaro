package angelus

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

type fanoutNode struct {
	id, parent string
	live       bool
	quota      int
	capped     bool
	ownerMade  bool
}

type fanoutRole struct {
	target string
	quota  int
}

type fanoutCache struct {
	mu      sync.Mutex
	parent  map[string]string
	live    map[string]bool
	kids    map[string]int
	held    map[string]int
	suspect map[string]bool
	own     map[string]int
	roles   map[string]fanoutRole
	eff     map[string]int
	used    map[string]int
	owners  map[string]bool
	pending map[string]int
	marks   map[string]fanoutHold
	writing map[string]chan struct{}
}

func newFanoutCache() *fanoutCache {
	return &fanoutCache{parent: map[string]string{}, live: map[string]bool{}, kids: map[string]int{}, held: map[string]int{},
		own: map[string]int{}, roles: map[string]fanoutRole{}, eff: map[string]int{}, used: map[string]int{}, owners: map[string]bool{}, pending: map[string]int{}, marks: map[string]fanoutHold{}, writing: map[string]chan struct{}{}}
}

func (c *fanoutCache) reset(nodes []fanoutNode, roles map[string]fanoutRole) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := fanoutTree(nodes, roles, c.pending)
	c.parent, c.live, c.kids, c.own, c.roles, c.eff, c.used, c.owners = t.parent, t.live, t.kids, t.own, t.roles, t.eff, t.used, t.owners
}

func fanoutTree(nodes []fanoutNode, roles map[string]fanoutRole, pending map[string]int) *fanoutCache {
	t := newFanoutCache()
	maps.Copy(t.pending, pending)
	for _, n := range nodes {
		t.parent[n.id], t.live[n.id] = n.parent, n.live
		if n.capped {
			t.own[n.id] = n.quota
		}
		if n.ownerMade {
			t.owners[n.id] = true
		}
	}
	maps.Copy(t.roles, roles)
	for id := range t.own {
		t.effOf(id)
	}
	for _, r := range roles {
		t.effOf(r.target)
	}
	waiting := map[string]int{}
	for _, n := range nodes {
		if _, known := t.parent[n.parent]; known {
			waiting[n.parent]++
		}
		if n.live && n.parent != "" {
			t.kids[n.parent]++
		}
	}
	var ready []string
	for _, n := range nodes {
		if waiting[n.id] == 0 {
			ready = append(ready, n.id)
		}
	}
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		p := t.parent[id]
		if _, known := t.parent[p]; !known {
			continue
		}
		t.used[p] += t.chargeOf(id)
		if waiting[p]--; waiting[p] == 0 {
			ready = append(ready, p)
		}
	}
	return t
}

func (c *fanoutCache) effOf(id string) {
	if id == "" {
		return
	}
	q, ok := c.own[id]
	for _, r := range c.roles {
		if r.target == id && (!ok || r.quota < q) {
			q, ok = r.quota, true
		}
	}
	if ok {
		c.eff[id] = q
	} else {
		delete(c.eff, id)
	}
}

func (c *fanoutCache) chargeOf(id string) int {
	if !c.live[id] {
		return c.spent(id)
	}
	if q, ok := c.eff[id]; ok {
		return 1 + max(q, c.spent(id))
	}
	return 1 + c.spent(id)
}

func (c *fanoutCache) absorbs(id string) bool {
	_, capped := c.eff[id]
	return capped && c.live[id]
}

func (c *fanoutCache) carry(from string, d int) {
	for id, steps := from, len(c.parent); id != "" && d != 0 && steps >= 0; steps-- {
		if _, known := c.parent[id]; !known {
			return
		}
		before := c.chargeOf(id)
		c.used[id] += d
		d = c.chargeOf(id) - before
		id = c.parent[id]
	}
}

func (c *fanoutCache) detach(id string) {
	p := c.parent[id]
	if c.live[id] && p != "" {
		c.kids[p]--
	}
	c.carry(p, -c.chargeOf(id))
}

func (c *fanoutCache) attach(id string) {
	p := c.parent[id]
	if c.live[id] && p != "" {
		c.kids[p]++
	}
	c.carry(p, c.chargeOf(id))
}

func (c *fanoutCache) put(n fanoutNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.parent[n.id]; known {
		c.detach(n.id)
	}
	c.parent[n.id], c.live[n.id] = n.parent, n.live
	if n.capped {
		c.own[n.id] = n.quota
	} else {
		delete(c.own, n.id)
	}
	if n.ownerMade {
		c.owners[n.id] = true
	}
	c.effOf(n.id)
	c.attach(n.id)
}

func (c *fanoutCache) setLive(id string, live bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.parent[id]; !known || c.live[id] == live {
		return
	}
	c.detach(id)
	c.live[id] = live
	c.attach(id)
}

func (c *fanoutCache) move(id, parent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.moveLocked(id, parent)
}

func (c *fanoutCache) moveLocked(id, parent string) {
	from, known := c.parent[id]
	if !known || from == parent {
		return
	}
	c.detach(id)
	c.parent[id] = parent
	c.attach(id)
}

func (c *fanoutCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.parent[id]; !known {
		return
	}
	c.detach(id)
	delete(c.parent, id)
	delete(c.live, id)
	delete(c.own, id)
	delete(c.eff, id)
	delete(c.used, id)
	delete(c.owners, id)
}

func (c *fanoutCache) relimit(id string, change func()) {
	_, known := c.parent[id]
	if known {
		c.detach(id)
	}
	change()
	c.effOf(id)
	if known {
		c.attach(id)
	}
}

func (c *fanoutCache) setOwn(id string, quota int, capped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if q, had := c.own[id]; had == capped && q == quota {
		return
	}
	c.relimit(id, func() {
		if capped {
			c.own[id] = quota
		} else {
			delete(c.own, id)
		}
	})
}

func (c *fanoutCache) setRole(role, target string, quota int, capped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, had := c.roles[role]
	if had == (capped && target != "") && old.target == target && old.quota == quota {
		return
	}
	if had {
		c.relimit(old.target, func() { delete(c.roles, role) })
	}
	if capped && target != "" {
		c.relimit(target, func() { c.roles[role] = fanoutRole{target: target, quota: quota} })
	}
}

func (c *fanoutCache) knowsRole(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.roles[id]
	return ok
}

func (c *fanoutCache) liveChildren(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kids[id]
}

func (c *fanoutCache) admitted(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kids[id] + c.held[id]
}

type fanoutHold struct {
	parent string
	slot   bool
	at     string
	charge int
}

func (c *fanoutCache) take(parent string, max int, capped bool, charge int, abdicating bool, judge func(error) error) (fanoutHold, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if capped && !abdicating {
		if n := c.kids[parent] + c.held[parent]; n >= max {
			return fanoutHold{}, maxChildrenRefusal(parent, n, max)
		}
	}
	if abdicating {
		charge = 0
		if err := c.overLocked(parent); err != nil {
			if err = judge(fmt.Errorf("%w; a succession that retires its predecessor fits only a budget that is not already overdrawn", err)); err != nil {
				return fanoutHold{}, err
			}
		}
	} else if err := judge(c.fitsLocked(parent, charge)); err != nil {
		return fanoutHold{}, err
	}
	h := fanoutHold{parent: parent, slot: capped}
	if capped {
		c.held[parent]++
	}
	h.at, h.charge = c.reserveLocked(parent, charge)
	return h, nil
}

func (c *fanoutCache) reserveLocked(from string, charge int) (string, int) {
	if charge <= 0 {
		return "", 0
	}
	at, ok := c.absorberOf(from)
	if !ok {
		return "", 0
	}
	c.addPendingLocked(at, charge)
	return at, charge
}

func (c *fanoutCache) mark(h fanoutHold, node string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.marks[node] = h
}

func (c *fanoutCache) markedAt(at string, moves []hierarchy.Move) int {
	n := 0
	for _, m := range moves {
		if h, ok := c.marks[m.Node]; ok && h.at == at {
			n += h.charge
		}
	}
	return n
}

func (c *fanoutCache) give(h fanoutHold, node string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.marks, node)
	if h.slot {
		if c.held[h.parent]--; c.held[h.parent] <= 0 {
			delete(c.held, h.parent)
		}
	}
	c.settleLocked(h.at, h.charge)
}

func (c *fanoutCache) settleLocked(at string, charge int) {
	if charge <= 0 {
		return
	}
	c.addPendingLocked(at, -charge)
}

func (c *fanoutCache) addPendingLocked(at string, d int) {
	before := c.chargeOf(at)
	if c.pending[at] += d; c.pending[at] <= 0 {
		delete(c.pending, at)
	}
	c.carry(c.parent[at], c.chargeOf(at)-before)
}

func (c *fanoutCache) spent(id string) int { return c.used[id] + c.pending[id] }

func maxChildrenRefusal(parent string, n, max int) error {
	return fmt.Errorf("fanout: %s holds %d live children of max_children %d; retire one, fork elsewhere, or have its parent grant more (figaro set --id %s %s %d)", parent, n, max, shortID(parent), form.FanoutMaxChildrenKey, n+1)
}

type fanoutBudget struct {
	used, quota int
	capped      bool
}

func (c *fanoutCache) budget(id string) fanoutBudget {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, capped := c.eff[id]
	return fanoutBudget{used: c.used[id], quota: q, capped: capped}
}

func (c *fanoutCache) fits(from string, need int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fitsLocked(from, need)
}

func (c *fanoutCache) fitsLocked(from string, need int) error {
	if need <= 0 {
		return nil
	}
	short := ""
	var seen []string
	for id, steps := from, len(c.parent); id != "" && steps >= 0; steps-- {
		if _, known := c.parent[id]; !known {
			break
		}
		if c.absorbs(id) {
			q := c.eff[id]
			seen = append(seen, fmt.Sprintf("%s has %d free of quota %d", shortID(id), max(q-c.spent(id), 0), q))
			if short == "" && len(seen) == 1 && c.spent(id)+need > q {
				short = id
			}
		}
		id = c.parent[id]
	}
	if short == "" {
		return nil
	}
	under := ""
	if made := c.ownerMadeUnder(short, 5); len(made) > 0 {
		under = " (the owner made " + strings.Join(made, ", ") + ")"
	}
	grant := fmt.Sprintf("figaro set --id %s %s %d", shortID(short), form.FanoutQuotaKey, c.spent(short)+need)
	return fmt.Errorf("fanout: this needs %d of budget, and %s; retire a figaro under it%s, or have its parent grant more (%s)", need, strings.Join(seen, ", "), under, grant)
}

func (c *fanoutCache) ownerMadeUnder(root string, most int) []string {
	var out []string
	for id := range c.owners {
		if !c.live[id] {
			continue
		}
		for up, steps := c.parent[id], len(c.parent); up != "" && steps >= 0; steps-- {
			if up == root {
				out = append(out, shortID(id))
				break
			}
			up = c.parent[up]
		}
	}
	sort.Strings(out)
	return out[:min(most, len(out))]
}

func (c *fanoutCache) over(from string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overLocked(from)
}

func (c *fanoutCache) overLocked(from string) error {
	if id, ok := c.absorberOf(from); ok && c.used[id] > c.eff[id] {
		return fmt.Errorf("fanout: %s already uses %d of quota %d", shortID(id), c.used[id], c.eff[id])
	}
	return nil
}

func (c *fanoutCache) absorberOf(from string) (string, bool) {
	for id, steps := from, len(c.parent); id != "" && steps >= 0; steps-- {
		if _, known := c.parent[id]; !known {
			return "", false
		}
		if c.absorbs(id) {
			return id, true
		}
		id = c.parent[id]
	}
	return "", false
}

func (c *fanoutCache) chargeChange(id string, quota int, capped bool) (from string, d int, used int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chargeChangeLocked(id, quota, capped)
}

func (c *fanoutCache) takeCharge(id string, quota int, capped bool, judge func(error) error) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.awaitWriteLocked(id)
	from, d, _ := c.chargeChangeLocked(id, quota, capped)
	if err := c.fitsLocked(from, d); err != nil {
		if err = judge(err); err != nil {
			return nil, err
		}
	}
	at, charge := c.reserveLocked(from, d)
	return c.writeLocked(id, at, charge), nil
}

func (c *fanoutCache) holdWrites(ids ...string) func() {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	c.mu.Lock()
	defer c.mu.Unlock()
	releases := make([]func(), 0, len(ids))
	for _, id := range ids {
		c.awaitWriteLocked(id)
		releases = append(releases, c.writeLocked(id, "", 0))
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
}

func (c *fanoutCache) awaitWriteLocked(id string) {
	for c.writing[id] != nil {
		inFlight := c.writing[id]
		c.mu.Unlock()
		<-inFlight
		c.mu.Lock()
	}
}

func (c *fanoutCache) writeLocked(id, at string, charge int) func() {
	landed := make(chan struct{})
	c.writing[id] = landed
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.settleLocked(at, charge)
			delete(c.writing, id)
			close(landed)
		})
	}
}

func (c *fanoutCache) chargeChangeLocked(id string, quota int, capped bool) (from string, d int, used int) {
	if _, known := c.parent[id]; !known {
		return "", 0, 0
	}
	used = c.used[id]
	if !c.live[id] {
		return "", 0, used
	}
	if q, held := c.roleCeiling(id); held && (!capped || q < quota) {
		quota, capped = q, true
	}
	after := 1 + c.spent(id)
	if capped {
		after = 1 + max(quota, c.spent(id))
	}
	return c.parent[id], after - c.chargeOf(id), used
}

func (c *fanoutCache) roleCeiling(id string) (int, bool) {
	var q int
	ok := false
	for _, r := range c.roles {
		if r.target == id && (!ok || r.quota < q) {
			q, ok = r.quota, true
		}
	}
	return q, ok
}

func (c *fanoutCache) fitsMoves(moves []hierarchy.Move) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := maps.Clone(c.used)
	type undo struct{ id, from string }
	var done []undo
	for _, m := range moves {
		from, known := c.parent[m.Node]
		if !known || from == m.To {
			continue
		}
		c.moveLocked(m.Node, m.To)
		done = append(done, undo{m.Node, from})
	}
	var err error
	for _, m := range moves {
		if id, ok := c.absorberOf(c.parent[m.Node]); ok && c.spent(id)-c.markedAt(id, moves) > c.eff[id] && c.used[id] > before[id] {
			err = fmt.Errorf("fanout: %s would use %d of quota %d; the reparent is refused and nothing moved", shortID(id), c.spent(id)-c.markedAt(id, moves), c.eff[id])
			break
		}
	}
	for i := len(done) - 1; i >= 0; i-- {
		c.moveLocked(done[i].id, done[i].from)
	}
	return err
}

func (c *fanoutCache) drift(nodes []fanoutNode, roles map[string]fanoutRole) []rpc.FanoutDrift {
	c.mu.Lock()
	defer c.mu.Unlock()
	tree := fanoutTree(nodes, roles, c.pending)
	var out []rpc.FanoutDrift
	seen := map[string]bool{}
	note := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		if c.describe(id) == tree.describe(id) {
			return
		}
		out = append(out, rpc.FanoutDrift{ID: id, Cached: c.describe(id), Tree: tree.describe(id)})
	}
	for _, m := range []map[string]int{c.kids, c.used, tree.kids, tree.used} {
		for id, n := range m {
			if n != 0 {
				note(id)
			}
		}
	}
	for id := range tree.parent {
		note(id)
	}
	for id := range c.parent {
		note(id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *fanoutCache) describe(id string) string {
	budget := "uncapped"
	if q, ok := c.eff[id]; ok {
		budget = fmt.Sprintf("quota %d", q)
	}
	parent, known := c.parent[id]
	if !known {
		return fmt.Sprintf("absent, %d live children, uses %d", c.kids[id], c.used[id])
	}
	return fmt.Sprintf("under %q, live %t, %d live children, uses %d, %s", parent, c.live[id], c.kids[id], c.used[id], budget)
}

func (a *Angelus) fanoutCounts() *fanoutCache {
	a.fanoutOnce.Do(func() {
		c := newFanoutCache()
		a.fanout = c
		if a.Backend == nil {
			return
		}
		a.Backend.Feed(store.TopologyFeed{
			Born:     func(id, parent string) { c.put(a.fanoutNodeOf(id, parent)) },
			Liveness: c.setLive,
			Limits:   func(id string) { a.fanoutLimitsChanged(c, id) },
		})
		c.reset(a.fanoutNodes(), a.fanoutRoles())
	})
	return a.fanout
}

func (a *Angelus) fanoutNodeOf(id, parent string) fanoutNode {
	n := fanoutNode{id: id, parent: parent, live: true}
	meta, _ := a.Backend.Meta(id)
	if meta != nil {
		n.live, n.ownerMade = meta.CountsAt(time.Now().UnixMilli()), meta.OwnerMade
		if meta.FanoutQuota != nil && a.chargesQuota(id) {
			n.quota, n.capped = *meta.FanoutQuota, true
		}
	}
	return n
}

func (a *Angelus) chargesQuota(id string) bool {
	n, ok := a.Backend.NodeLink(id)
	return ok && n.Kind == store.KindConversation
}

func (a *Angelus) fanoutLimitsChanged(c *fanoutCache, id string) {
	roles, _ := a.roles()
	if target, isRole := roles.forms[id]; isRole || c.knowsRole(id) {
		q, ok := 0, false
		if snap, err := a.Backend.FormState(id); err == nil && !routingAlias(snap) {
			q, ok, _ = form.FanoutQuota(snap)
		}
		c.setRole(id, target, q, ok)
		return
	}
	if !a.chargesQuota(id) {
		return
	}
	meta, _ := a.Backend.Meta(id)
	if meta != nil && meta.FanoutQuota != nil {
		c.setOwn(id, *meta.FanoutQuota, true)
		return
	}
	c.setOwn(id, 0, false)
}

func (a *Angelus) fanoutNodes() []fanoutNode {
	h := a.topology()
	views := a.Backend.EveryNode()
	out := make([]fanoutNode, 0, len(views))
	for _, v := range views {
		parent := v.Parent
		if p, ok := h.Drawn(v.ID); ok {
			parent = p
		}
		n := fanoutNode{id: v.ID, parent: parent, live: true}
		if meta, _ := a.Backend.Meta(v.ID); meta != nil {
			n.live, n.ownerMade = meta.CountsAt(time.Now().UnixMilli()), meta.OwnerMade
			if meta.FanoutQuota != nil && v.Kind == store.KindConversation {
				n.quota, n.capped = *meta.FanoutQuota, true
			}
		}
		out = append(out, n)
	}
	return out
}

func (a *Angelus) fanoutRoles() map[string]fanoutRole {
	roles, _ := a.roles()
	out := map[string]fanoutRole{}
	for id, target := range roles.forms {
		snap, err := a.Backend.FormState(id)
		if err != nil || routingAlias(snap) {
			continue
		}
		if q, ok, _ := form.FanoutQuota(snap); ok {
			out[id] = fanoutRole{target: target, quota: q}
		}
	}
	return out
}

func (a *Angelus) settledFanout() *fanoutCache {
	c := a.fanoutCounts()
	keeper, ok := a.Backend.(ttlKeeper)
	if !ok {
		return c
	}
	now := time.Now().UnixMilli()
	for _, e := range keeper.TTLDue(now) {
		if e.Expired(now) {
			c.setLive(e.ID, false)
		}
	}
	return c
}

func (a *Angelus) liveChildCount(id string) int {
	return a.settledFanout().admitted(id)
}

func (a *Angelus) holdFork(parent string, charge int, abdicating bool) (func(), error) {
	release, _, err := a.holdBirth(parent, charge, abdicating)
	return release, err
}

func (a *Angelus) holdBirth(parent string, charge int, abdicating bool) (release func(), forNode func(string), err error) {
	max, capped, err := a.fanoutLimit(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("fork: the limit of %s cannot be read to admit the fork: %w", parent, err)
	}
	c := a.settledFanout()
	h, err := c.take(parent, max, capped, charge, abdicating, func(err error) error { return a.Policies.Shadow(authz.Quota, err) })
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	node := ""
	return func() { once.Do(func() { c.give(h, node) }) }, func(id string) { node = id; c.mark(h, id) }, nil
}

func (a *Angelus) settleFanout(p hierarchy.Plan) {
	c := a.fanoutCounts()
	for _, m := range p.Moves {
		c.move(m.Node, m.To)
	}
	for _, id := range p.Delete {
		if a.nodeKept(id) {
			c.setLive(id, false)
			continue
		}
		c.forget(id)
	}
	for _, id := range p.Revive {
		c.put(a.fanoutNodeOf(id, a.drawnParentOf(id)))
	}
}

func (a *Angelus) nodeKept(id string) bool {
	if a.Backend == nil {
		return false
	}
	_, ok := a.Backend.NodeLink(id)
	return ok
}

func (a *Angelus) drawnParentOf(id string) string {
	if p, ok := a.topology().Drawn(id); ok {
		return p
	}
	v, _ := a.Backend.NodeLink(id)
	return v.Parent
}

func (a *Angelus) fanoutDrift() []rpc.FanoutDrift {
	if a.Backend == nil {
		return nil
	}
	var out []rpc.FanoutDrift
	_ = a.topology().WithLock(func() error {
		out = a.fanoutCounts().drift(a.fanoutNodes(), a.fanoutRoles())
		return nil
	})
	return out
}

func (a *Angelus) reconcileFanout() {
	if a.Backend == nil {
		return
	}
	c := a.settledFanout()
	nodes, roles := a.fanoutNodes(), a.fanoutRoles()
	if d := c.drift(nodes, roles); c.driftPersists(d) {
		slog.Warn("fanout: the cached counts drifted from the tree across two sweeps and were rebuilt", "nodes", len(d), "first", d[0].ID)
		c.reset(nodes, roles)
	}
	if over := a.fanoutOver(); len(over) > 0 {
		slog.Info("fanout: over budget, so no forks until under", "nodes", len(over), "first", over[0].ID, "over", over[0].Over)
	}
}

func (c *fanoutCache) driftPersists(d []rpc.FanoutDrift) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := c.suspect
	c.suspect = make(map[string]bool, len(d))
	persists := false
	for _, x := range d {
		c.suspect[x.ID] = true
		persists = persists || before[x.ID]
	}
	if persists {
		c.suspect = nil
	}
	return persists
}

func (c *fanoutCache) overQuota() []rpc.FanoutOver {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []rpc.FanoutOver
	for id, q := range c.eff {
		if c.live[id] && c.used[id] > q {
			out = append(out, rpc.FanoutOver{ID: id, Over: fmt.Sprintf("uses %d of quota %d", c.used[id], q)})
		}
	}
	return out
}

func (c *fanoutCache) parents() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for id, n := range c.kids {
		if n > 0 && c.live[id] {
			out[id] = n
		}
	}
	return out
}

func (a *Angelus) fanoutOver() []rpc.FanoutOver {
	if a.Backend == nil {
		return nil
	}
	c := a.settledFanout()
	out := c.overQuota()
	for id, n := range c.parents() {
		if max, ok, err := a.fanoutLimit(id); err == nil && ok && n > max {
			out = append(out, rpc.FanoutOver{ID: id, Over: fmt.Sprintf("holds %d live children of max_children %d", n, max)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID+out[i].Over < out[j].ID+out[j].Over })
	return out
}

func shortID(id string) string { return id[:min(8, len(id))] }

func (a *Angelus) fanoutUnmirrored() []rpc.FanoutUnmirrored {
	if a.Backend == nil {
		return nil
	}
	var out []rpc.FanoutUnmirrored
	for _, id := range a.Backend.ConversationIDs() {
		if meta, _ := a.Backend.Meta(id); meta != nil && meta.FanoutQuota != nil {
			continue
		}
		snap, err := a.Backend.FormState(id)
		if err != nil {
			continue
		}
		if raw, ok := snap.Get(form.FanoutQuotaKey); ok {
			out = append(out, rpc.FanoutUnmirrored{ID: id, Quota: form.ScalarString(raw)})
		}
	}
	return out
}
