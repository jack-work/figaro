package angelus

import (
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

type fanoutNode struct {
	id, parent string
	live       bool
	quota      int
	capped     bool
}

type fanoutRole struct {
	target string
	quota  int
}

type fanoutCache struct {
	mu     sync.Mutex
	parent map[string]string
	live   map[string]bool
	kids   map[string]int
	own    map[string]int
	roles  map[string]fanoutRole
	eff    map[string]int
	used   map[string]int
}

func newFanoutCache() *fanoutCache {
	return &fanoutCache{parent: map[string]string{}, live: map[string]bool{}, kids: map[string]int{},
		own: map[string]int{}, roles: map[string]fanoutRole{}, eff: map[string]int{}, used: map[string]int{}}
}

func (c *fanoutCache) reset(nodes []fanoutNode, roles map[string]fanoutRole) {
	t := newFanoutCache()
	for _, n := range nodes {
		t.parent[n.id], t.live[n.id] = n.parent, n.live
		if n.capped {
			t.own[n.id] = n.quota
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
	c.mu.Lock()
	c.parent, c.live, c.kids, c.own, c.roles, c.eff, c.used = t.parent, t.live, t.kids, t.own, t.roles, t.eff, t.used
	c.mu.Unlock()
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
		return c.used[id]
	}
	if q, ok := c.eff[id]; ok {
		return 1 + q
	}
	return 1 + c.used[id]
}

func (c *fanoutCache) absorbs(id string) bool {
	_, capped := c.eff[id]
	return capped && c.live[id]
}

func (c *fanoutCache) carry(from string, d int) {
	if d == 0 {
		return
	}
	for id, steps := from, len(c.parent); id != "" && steps >= 0; steps-- {
		if _, known := c.parent[id]; !known {
			return
		}
		c.used[id] += d
		if c.absorbs(id) {
			return
		}
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
	short := false
	var seen []string
	charged := true
	for id, steps := from, len(c.parent); id != "" && steps >= 0; steps-- {
		if _, known := c.parent[id]; !known {
			break
		}
		if c.absorbs(id) {
			q := c.eff[id]
			seen = append(seen, fmt.Sprintf("%s has %d free of quota %d", shortID(id), max(q-c.used[id], 0), q))
			if charged && c.used[id]+need > q {
				short = true
			}
			charged = false
		}
		id = c.parent[id]
	}
	if !short {
		return nil
	}
	return fmt.Errorf("fanout: this needs %d of budget, and %s; retire a figaro under it or ask your parent for more", need, strings.Join(seen, ", "))
}

func (c *fanoutCache) over(from string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	after := 1 + used
	if capped {
		after = 1 + quota
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
		if id, ok := c.absorberOf(c.parent[m.Node]); ok && c.used[id] > c.eff[id] && c.used[id] > before[id] {
			err = fmt.Errorf("fanout: %s would use %d of quota %d; the reparent is refused and nothing moved", shortID(id), c.used[id], c.eff[id])
			break
		}
	}
	for i := len(done) - 1; i >= 0; i-- {
		c.moveLocked(done[i].id, done[i].from)
	}
	return err
}

func (c *fanoutCache) drift(nodes []fanoutNode, roles map[string]fanoutRole) []rpc.FanoutDrift {
	tree := newFanoutCache()
	tree.reset(nodes, roles)
	c.mu.Lock()
	defer c.mu.Unlock()
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
		n.live = meta.RetiredAtMS == 0 && meta.ReapedAtMS == 0
		if meta.FanoutQuota != nil && a.chargesQuota(id) {
			n.quota, n.capped = *meta.FanoutQuota, true
		}
	}
	return n
}

func (a *Angelus) chargesQuota(id string) bool {
	n, ok := a.Backend.Node(id)
	return ok && n.Kind == store.KindConversation
}

func (a *Angelus) fanoutLimitsChanged(c *fanoutCache, id string) {
	roles, _ := a.roles()
	if target, isRole := roles.forms[id]; isRole || c.knowsRole(id) {
		q, ok := 0, false
		if snap, err := a.Backend.FormState(id); err == nil {
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
			n.live = meta.RetiredAtMS == 0 && meta.ReapedAtMS == 0
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
		if err != nil {
			continue
		}
		if q, ok, _ := form.FanoutQuota(snap); ok {
			out[id] = fanoutRole{target: target, quota: q}
		}
	}
	return out
}

func (a *Angelus) liveChildCount(id string) int {
	return a.fanoutCounts().liveChildren(id)
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
	_, ok := a.Backend.Node(id)
	return ok
}

func (a *Angelus) drawnParentOf(id string) string {
	if p, ok := a.topology().Drawn(id); ok {
		return p
	}
	v, _ := a.Backend.Node(id)
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
	_ = a.topology().WithLock(func() error {
		nodes, roles := a.fanoutNodes(), a.fanoutRoles()
		c := a.fanoutCounts()
		if d := c.drift(nodes, roles); len(d) > 0 {
			slog.Warn("fanout: the cached counts drifted from the tree and were rebuilt", "nodes", len(d), "first", d[0].ID)
			c.reset(nodes, roles)
		}
		return nil
	})
}

func shortID(id string) string { return id[:min(8, len(id))] }
