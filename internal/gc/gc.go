package gc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const PlanLife = 10 * time.Minute

var (
	ErrNoPlan       = errors.New("no such plan")
	ErrNotYourPlan  = errors.New("the plan belongs to another caller")
	ErrOutsideBox   = errors.New("a box plans only in its own box")
	ErrSetChanged   = errors.New("the nodes it would take changed since the plan")
	ErrHeld         = errors.New("held at the reap")
	ErrUnknownUnder = errors.New("under names nothing in this store")
)

type Selector struct {
	Idle   time.Duration
	Empty  bool
	Older  time.Duration
	Outfit string
	Under  string
	Match  string
	Box    string
}

func (s Selector) ChoosesArias() bool {
	return s.Idle > 0 || s.Empty || s.Older > 0 || s.Outfit != "" || s.Under != "" || s.Match != "" || s.Box != ""
}

type Caller struct {
	Principal string
	Box       string
	MayKill   func(id string) error
}

type Row struct {
	ID        string
	Parent    string
	Outfit    string
	Mantra    string
	Cwd       string
	CreatedMS int64
	LastUseMS int64
	Messages  int
}

type Stump struct {
	ID       string
	Outfit   string
	Version  string
	Children int
}

type World interface {
	Now() time.Time
	Rows() []Row
	Parent(id string) (string, bool)
	Reaped(id string) bool
	Handle(id string) string
	Empty(id string) bool
	Box(id string) string
	DeleteSet(id string) []string
	Hold(set []string, root string) (by, why string, err error)
	Bytes(id string) int64
	Stumps() []Stump
	Collect(stump string) error
	Reap(root string, expect []string, stamp string) error
}

type Candidate struct {
	ID       string
	Handle   string
	Mantra   string
	Idle     time.Duration
	Messages int
	Bytes    int64
	Set      []string
}

type Kept struct {
	ID     string
	Handle string
	Mantra string
	By     string
	Why    string
}

type Plan struct {
	Run        string
	At         time.Time
	Selector   Selector
	Caller     Caller
	Candidates []Candidate
	Kept       []Kept
	Stumps     []Stump
}

func (p Plan) Expires() time.Time { return p.At.Add(PlanLife) }

func (p Plan) Collectable() []Stump {
	var out []Stump
	for _, s := range p.Stumps {
		if s.Children == 0 {
			out = append(out, s)
		}
	}
	return out
}

type Dropped struct {
	ID     string
	Handle string
	Why    string
}

type Result struct {
	Run       string
	Reaped    []Candidate
	Dropped   []Dropped
	Collected []Stump
	Failed    []Dropped
}

type Collector struct {
	world World
	now   func() time.Time

	apply sync.Mutex

	mu    sync.Mutex
	plans map[string]Plan
}

func New(w World) *Collector {
	return &Collector{world: w, now: w.Now, plans: map[string]Plan{}}
}

func (c *Collector) Plan(sel Selector, caller Caller) (Plan, error) {
	p, err := c.plan(sel, caller, true)
	if err != nil {
		return Plan{}, err
	}
	p.Run = runID(p.At)
	c.mu.Lock()
	defer c.mu.Unlock()
	for run, old := range c.plans {
		if !old.Expires().After(p.At) {
			delete(c.plans, run)
		}
	}
	c.plans[p.Run] = p
	return p, nil
}

func (c *Collector) Take(run, principal string) (Plan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.plans[run]
	if !ok || !p.Expires().After(c.now()) {
		delete(c.plans, run)
		return Plan{}, fmt.Errorf("%w: %s (a plan lasts %s; plan again)", ErrNoPlan, run, PlanLife)
	}
	if p.Caller.Principal != principal {
		return Plan{}, fmt.Errorf("%w: %s", ErrNotYourPlan, run)
	}
	delete(c.plans, run)
	return p, nil
}

func runID(at time.Time) string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return at.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

func (c *Collector) plan(sel Selector, caller Caller, sizes bool) (Plan, error) {
	if caller.Box != "" && sel.Box != "" && sel.Box != caller.Box {
		return Plan{}, fmt.Errorf("%w: box %s asked for box %s", ErrOutsideBox, caller.Box, sel.Box)
	}
	w := c.world
	p := Plan{Selector: sel, Caller: caller, At: c.now()}
	if !sel.ChoosesArias() {
		if caller.Box == "" {
			p.Stumps = w.Stumps()
		}
		return p, nil
	}
	if sel.Under != "" {
		if _, ok := w.Parent(sel.Under); !ok {
			return Plan{}, fmt.Errorf("%w: %s", ErrUnknownUnder, sel.Under)
		}
	}
	match := newMatcher(sel.Match)
	chosen := map[string]Row{}
	var order []string
	for _, r := range w.Rows() {
		if !c.matches(sel, r, p.At, match) {
			continue
		}
		if !inScope(sel, caller, w.Box(r.ID)) {
			continue
		}
		if caller.MayKill != nil && caller.MayKill(r.ID) != nil {
			continue
		}
		chosen[r.ID] = r
		order = append(order, r.ID)
	}
	sets := make(map[string][]string, len(order))
	for _, id := range order {
		sets[id] = orSelf(w.DeleteSet(id), id)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if a, b := len(sets[order[i]]), len(sets[order[j]]); a != b {
			return a > b
		}
		return order[i] < order[j]
	})
	covered := map[string]bool{}
	for _, id := range order {
		if covered[id] {
			continue
		}
		r, set := chosen[id], sets[id]
		if by := c.unchosen(set, id, chosen); by != "" {
			if !c.visible(caller, by) {
				p.Kept = append(p.Kept, c.kept(r, "", "it would take a figaro outside this box"))
				continue
			}
			p.Kept = append(p.Kept, c.kept(r, by, "it would take "+c.name(by)+", which this selection does not choose"))
			continue
		}
		by, why, err := w.Hold(set, id)
		if err != nil {
			return Plan{}, err
		}
		if why != "" {
			switch {
			case !c.visible(caller, by):
				by, why = "", "a figaro outside this box: "+why
			case by != id:
				why = c.name(by) + ": " + why
			}
			p.Kept = append(p.Kept, c.kept(r, by, why))
			continue
		}
		cand := Candidate{
			ID: id, Handle: w.Handle(id), Mantra: r.Mantra,
			Idle: idleFor(r, p.At), Messages: r.Messages, Set: set,
		}
		if sizes {
			for _, n := range set {
				cand.Bytes += w.Bytes(n)
			}
		}
		for _, n := range set {
			covered[n] = true
		}
		p.Candidates = append(p.Candidates, cand)
	}
	sort.SliceStable(p.Candidates, func(i, j int) bool { return p.Candidates[i].Idle > p.Candidates[j].Idle })
	return p, nil
}

func (c *Collector) unchosen(set []string, root string, chosen map[string]Row) string {
	for _, n := range set {
		if n == root {
			continue
		}
		if _, ok := chosen[n]; !ok && !c.world.Reaped(n) {
			return n
		}
	}
	return ""
}

func (c *Collector) visible(caller Caller, id string) bool {
	return caller.Box == "" || c.world.Box(id) == caller.Box
}

func (c *Collector) kept(r Row, by, why string) Kept {
	return Kept{ID: r.ID, Handle: c.world.Handle(r.ID), Mantra: r.Mantra, By: by, Why: why}
}

func (c *Collector) name(id string) string {
	if h := c.world.Handle(id); h != "" {
		return h
	}
	return id
}

func (c *Collector) matches(sel Selector, r Row, now time.Time, match *matcher) bool {
	if sel.Idle > 0 && idleFor(r, now) < sel.Idle {
		return false
	}
	if sel.Older > 0 && (r.CreatedMS == 0 || now.Sub(time.UnixMilli(r.CreatedMS)) < sel.Older) {
		return false
	}
	if sel.Outfit != "" && r.Outfit != sel.Outfit {
		return false
	}
	if sel.Under != "" && !c.under(r, sel.Under) {
		return false
	}
	if match != nil && !match.matches(r.Mantra, c.world.Handle(r.ID), r.Cwd) {
		return false
	}
	if sel.Empty && !c.world.Empty(r.ID) {
		return false
	}
	return true
}

func (c *Collector) under(r Row, root string) bool {
	seen := map[string]bool{r.ID: true}
	for up := r.Parent; up != "" && !seen[up]; {
		if up == root {
			return true
		}
		seen[up] = true
		next, ok := c.world.Parent(up)
		if !ok {
			return false
		}
		up = next
	}
	return false
}

func inScope(sel Selector, caller Caller, box string) bool {
	if caller.Box != "" {
		return box == caller.Box
	}
	if sel.Box != "" {
		return box == sel.Box
	}
	return box == ""
}

func idleFor(r Row, now time.Time) time.Duration {
	return max(now.Sub(time.UnixMilli(r.LastUseMS)), 0)
}

func orSelf(ids []string, id string) []string {
	if len(ids) == 0 {
		return []string{id}
	}
	return ids
}

func (c *Collector) Apply(p Plan) (Result, error) {
	c.apply.Lock()
	defer c.apply.Unlock()
	w := c.world
	res := Result{Run: p.Run}
	if stumps := p.Collectable(); len(stumps) > 0 {
		now := map[string]Stump{}
		for _, s := range w.Stumps() {
			now[s.ID] = s
		}
		for _, s := range stumps {
			cur, ok := now[s.ID]
			switch {
			case !ok:
				continue
			case cur.Children > 0:
				res.Dropped = append(res.Dropped, Dropped{ID: s.ID, Why: "an aria was born under it since the plan"})
				continue
			}
			if err := w.Collect(s.ID); err != nil {
				res.Failed = append(res.Failed, Dropped{ID: s.ID, Why: err.Error()})
				continue
			}
			res.Collected = append(res.Collected, s)
		}
	}
	if len(p.Candidates) == 0 {
		return res, nil
	}
	fresh, err := c.plan(p.Selector, p.Caller, false)
	if err != nil {
		return res, err
	}
	again := map[string]Candidate{}
	goesWith := map[string]string{}
	for _, f := range fresh.Candidates {
		again[f.ID] = f
		for _, n := range f.Set {
			goesWith[n] = f.ID
		}
	}
	held := map[string]Kept{}
	for _, k := range fresh.Kept {
		held[k.ID] = k
	}
	for _, cand := range p.Candidates {
		f, ok := again[cand.ID]
		drop := func(why string) {
			res.Dropped = append(res.Dropped, Dropped{ID: cand.ID, Handle: cand.Handle, Why: why})
		}
		switch {
		case !ok && held[cand.ID].Why != "":
			drop("kept now: " + held[cand.ID].Why)
			continue
		case !ok && goesWith[cand.ID] != "":
			drop("it now goes with " + c.name(goesWith[cand.ID]) + ", which this plan did not take")
			continue
		case !ok:
			drop("no longer chosen")
			continue
		case !sameMembers(f.Set, cand.Set):
			drop(ErrSetChanged.Error())
			continue
		}
		if refused := c.refused(p.Caller, cand.Set); refused != nil {
			drop(refused.Error())
			continue
		}
		if err := w.Reap(cand.ID, cand.Set, "gc:"+p.Run); err != nil {
			if errors.Is(err, ErrSetChanged) {
				drop(ErrSetChanged.Error())
				continue
			}
			if errors.Is(err, ErrHeld) {
				drop(err.Error())
				continue
			}
			res.Failed = append(res.Failed, Dropped{ID: cand.ID, Handle: cand.Handle, Why: err.Error()})
			continue
		}
		res.Reaped = append(res.Reaped, cand)
	}
	return res, nil
}

func (c *Collector) refused(caller Caller, set []string) error {
	if caller.MayKill == nil {
		return nil
	}
	for _, n := range set {
		if err := caller.MayKill(n); err != nil {
			return err
		}
	}
	return nil
}

func sameMembers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}
