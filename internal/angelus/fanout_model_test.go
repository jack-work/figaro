package angelus

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/jack-work/figaro/internal/hierarchy"
)

type modelNode struct {
	parent string
	live   bool
	quota  int
	capped bool
}

type quotaModel map[string]*modelNode

func (m quotaModel) used(id string) int {
	n := 0
	for c, node := range m {
		if node.parent == id {
			n += m.charge(c)
		}
	}
	return n
}

func (m quotaModel) charge(id string) int {
	n := m[id]
	switch {
	case !n.live:
		return m.used(id)
	case n.capped:
		return 1 + n.quota
	}
	return 1 + m.used(id)
}

func (m quotaModel) nodes() []fanoutNode {
	var out []fanoutNode
	for id, n := range m {
		out = append(out, fanoutNode{id: id, parent: n.parent, live: n.live, quota: n.quota, capped: n.capped})
	}
	return out
}

func (m quotaModel) live() []string {
	var out []string
	for id, n := range m {
		if n.live {
			out = append(out, id)
		}
	}
	return out
}

func (m quotaModel) under(id, root string) bool {
	for cur := id; cur != ""; cur = m[cur].parent {
		if cur == root {
			return true
		}
	}
	return false
}

func requireModel(t *testing.T, c *fanoutCache, m quotaModel, step string) {
	t.Helper()
	for id, n := range m {
		b := c.budget(id)
		if want := m.used(id); b.used != want {
			t.Fatalf("%s: %s uses %d in the cache, %d in the model", step, id, b.used, want)
		}
		if n.live && n.capped && b.used > n.quota {
			t.Fatalf("%s: %s uses %d of quota %d", step, id, b.used, n.quota)
		}
	}
	if d := c.drift(m.nodes(), nil); len(d) != 0 {
		t.Fatalf("%s: the cache drifted from a rebuild: %+v", step, d)
	}
}

// Random forks, retirements, reparents, re-grants and hard successions keep
// the cache equal to a model that recounts the tree, and no live capped
// figaro ever uses more than its quota.
func TestRandomTopologyChangesKeepEveryQuotaAgainstAModel(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		m := quotaModel{"king": {live: true, quota: 12, capped: true}}
		c := newFanoutCache()
		c.reset(m.nodes(), nil)
		next := 0
		for step := range 300 {
			live := m.live()
			x := live[r.IntN(len(live))]
			name := fmt.Sprintf("seed %d step %d", seed, step)
			switch op := r.IntN(10); {
			case op < 4:
				n := &modelNode{parent: x, live: true}
				if r.IntN(2) == 0 {
					n.quota, n.capped = r.IntN(3), true
				}
				charge := 1
				if n.capped {
					charge += n.quota
				}
				if c.fits(x, charge) != nil {
					continue
				}
				next++
				id := fmt.Sprintf("n%d", next)
				m[id] = n
				c.put(fanoutNode{id: id, parent: x, live: true, quota: n.quota, capped: n.capped})
			case op < 6:
				if x == "king" {
					continue
				}
				m[x].live = false
				c.setLive(x, false)
			case op < 8:
				to := live[r.IntN(len(live))]
				if x == "king" || m.under(to, x) {
					continue
				}
				if c.fitsMoves([]hierarchy.Move{{Node: x, To: to}}) != nil {
					continue
				}
				m[x].parent = to
				c.move(x, to)
			case op < 9:
				if x == "king" {
					continue
				}
				q := r.IntN(4)
				from, d, used := c.chargeChange(x, q, true)
				if q < used || c.fits(from, d) != nil {
					continue
				}
				m[x].quota, m[x].capped = q, true
				c.setOwn(x, q, true)
			default:
				if x == "king" || c.over(x) != nil {
					continue
				}
				next++
				y := fmt.Sprintf("n%d", next)
				m[y] = &modelNode{parent: x, live: true, capped: true}
				c.put(fanoutNode{id: y, parent: x, live: true, capped: true})
				gp := m[x].parent
				m[y].parent, m[x].parent = gp, y
				c.move(y, gp)
				c.move(x, y)
				m[y].quota, m[y].capped = m[x].quota, m[x].capped
				c.setOwn(y, m[x].quota, m[x].capped)
				m[x].capped = false
				c.setOwn(x, 0, false)
				m[x].live = false
				c.setLive(x, false)
			}
			requireModel(t, c, m, name)
		}
	}
}
