package gc

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeNode struct {
	row    Row
	parent string
	box    string
	empty  bool
	reaped bool
	hold   string
	bytes  int64
}

type fakeWorld struct {
	now     time.Time
	nodes   map[string]*fakeNode
	stumps  []Stump
	reaps   []string
	stamps  map[string]string
	onReap  func(root string)
	reapErr error
}

func newFake() *fakeWorld {
	return &fakeWorld{now: time.Unix(1_800_000_000, 0), nodes: map[string]*fakeNode{}, stamps: map[string]string{}}
}

func (f *fakeWorld) add(id, parent string, idle, age time.Duration) *fakeNode {
	n := &fakeNode{parent: parent, row: Row{
		ID: id, Parent: parent, Outfit: "mock",
		CreatedMS: f.now.Add(-age).UnixMilli(), LastUseMS: f.now.Add(-idle).UnixMilli(), Messages: 4,
	}, bytes: 100}
	f.nodes[id] = n
	return n
}

func (f *fakeWorld) Now() time.Time { return f.now }

func (f *fakeWorld) Rows() []Row {
	var out []Row
	for _, id := range f.ids() {
		if n := f.nodes[id]; !n.reaped {
			out = append(out, n.row)
		}
	}
	return out
}

func (f *fakeWorld) ids() []string {
	var ids []string
	for id := range f.nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (f *fakeWorld) Parent(id string) (string, bool) {
	n, ok := f.nodes[id]
	if !ok {
		return "", id == "form-1"
	}
	return n.parent, true
}

func (f *fakeWorld) Reaped(id string) bool   { return f.nodes[id] != nil && f.nodes[id].reaped }
func (f *fakeWorld) Handle(id string) string { return "h-" + id }
func (f *fakeWorld) Empty(id string) bool    { return f.nodes[id].empty }
func (f *fakeWorld) Box(id string) string    { return f.nodes[id].box }
func (f *fakeWorld) Bytes(id string) int64   { return f.nodes[id].bytes }

func (f *fakeWorld) DeleteSet(id string) []string {
	out := []string{id}
	for _, c := range f.ids() {
		if f.nodes[c].parent == id {
			out = append(out, f.DeleteSet(c)...)
		}
	}
	return out
}

func (f *fakeWorld) Hold(set []string, root string) (string, string, error) {
	for _, id := range set {
		if n := f.nodes[id]; n.hold != "" {
			return id, n.hold, nil
		}
	}
	return "", "", nil
}

func (f *fakeWorld) Stumps() []Stump         { return f.stumps }
func (f *fakeWorld) Collect(id string) error { f.reaps = append(f.reaps, "stump:"+id); return nil }

func (f *fakeWorld) Reap(root string, expect []string, stamp string) error {
	if f.onReap != nil {
		f.onReap(root)
	}
	if f.reapErr != nil {
		return f.reapErr
	}
	if !sameMembers(f.DeleteSet(root), expect) {
		return ErrSetChanged
	}
	for _, id := range expect {
		f.nodes[id].reaped = true
		f.stamps[id] = stamp
	}
	f.reaps = append(f.reaps, root)
	return nil
}

func owner() Caller { return Caller{Principal: "user:1000"} }

func candidates(p Plan) []string {
	var out []string
	for _, c := range p.Candidates {
		out = append(out, c.ID)
	}
	slices.Sort(out)
	return out
}

func keptWhy(p Plan, id string) string {
	for _, k := range p.Kept {
		if k.ID == id {
			return k.Why
		}
	}
	return ""
}

func TestEachSelectorFieldChoosesOnItsOwn(t *testing.T) {
	f := newFake()
	f.add("old", "", 40*24*time.Hour, 50*24*time.Hour)
	f.add("fresh", "", time.Hour, 50*24*time.Hour)
	f.add("young", "", 40*24*time.Hour, time.Hour)
	f.add("blank", "", 40*24*time.Hour, 50*24*time.Hour).empty = true
	f.add("opus", "", 40*24*time.Hour, 50*24*time.Hour).row.Outfit = "opus"
	f.add("tapes", "", 40*24*time.Hour, 50*24*time.Hour).row.Mantra = "recording the tapes"
	f.add("incwd", "", 40*24*time.Hour, 50*24*time.Hour).row.Cwd = "/home/gluck/dev/tapes"
	f.add("under", "form-1", 40*24*time.Hour, 50*24*time.Hour)
	f.add("boxed", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	all := []string{"blank", "fresh", "incwd", "old", "opus", "tapes", "under", "young"}
	without := func(drop ...string) []string {
		return slices.DeleteFunc(slices.Clone(all), func(s string) bool { return slices.Contains(drop, s) })
	}
	for _, tc := range []struct {
		name string
		sel  Selector
		want []string
	}{
		{"idle", Selector{Idle: 30 * 24 * time.Hour}, without("fresh")},
		{"older", Selector{Older: 24 * time.Hour}, without("young")},
		{"empty", Selector{Empty: true}, []string{"blank"}},
		{"outfit", Selector{Outfit: "opus"}, []string{"opus"}},
		{"match on mantra and cwd", Selector{Match: "tapes"}, []string{"incwd", "tapes"}},
		{"match on handle", Selector{Match: "'h-opus"}, []string{"opus"}},
		{"a negated term", Selector{Match: "tapes !gluck"}, []string{"tapes"}},
		{"match on id", Selector{Match: "^fresh"}, []string{"fresh"}},
		{"letters spread through a mantra choose nothing", Selector{Match: "rcdtps"}, nil},
		{"under", Selector{Under: "form-1"}, []string{"under"}},
		{"box", Selector{Box: "judge"}, []string{"boxed"}},
		{"fields AND together", Selector{Idle: 30 * 24 * time.Hour, Older: 24 * time.Hour, Match: "tapes"}, []string{"incwd", "tapes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(f).Plan(tc.sel, owner())
			if err != nil {
				t.Fatal(err)
			}
			if got := candidates(p); !slices.Equal(got, tc.want) {
				t.Fatalf("chose %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNoSelectorChoosesNoFigaroOnlyStumps(t *testing.T) {
	f := newFake()
	f.add("old", "", 40*24*time.Hour, 50*24*time.Hour)
	f.stumps = []Stump{{ID: "mock@1", Children: 1}, {ID: "mock@0"}}
	c := New(f)
	p, err := c.Plan(Selector{}, owner())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 0 || len(p.Collectable()) != 1 {
		t.Fatalf("candidates %v, collectable %v", p.Candidates, p.Collectable())
	}
	res, err := c.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.reaps, []string{"stump:mock@0"}) {
		t.Fatalf("did %v", f.reaps)
	}
	if len(res.Collected) != 1 {
		t.Fatalf("collected %v", res.Collected)
	}
}

func TestABoxSeesOnlyItsBoxAndTheOwnerSeesBoxesOnlyByName(t *testing.T) {
	f := newFake()
	f.add("mine", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	f.add("theirs", "", 40*24*time.Hour, 50*24*time.Hour).box = "jury"
	f.add("host", "", 40*24*time.Hour, 50*24*time.Hour)
	f.stumps = []Stump{{ID: "mock@0"}}
	sel := Selector{Idle: time.Hour}
	box := Caller{Principal: "sandbox:judge@local:box", Box: "judge"}
	p, err := New(f).Plan(sel, box)
	if err != nil {
		t.Fatal(err)
	}
	if got := candidates(p); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("the box chose %v", got)
	}
	if _, err := New(f).Plan(Selector{Box: "jury"}, box); !errors.Is(err, ErrOutsideBox) {
		t.Fatalf("a box asked for another box: %v", err)
	}
	if p, _ := New(f).Plan(Selector{}, box); len(p.Stumps) != 0 {
		t.Fatal("a box was offered the store's outfit versions")
	}
	if got := candidates(mustPlan(t, New(f), sel, owner())); !slices.Equal(got, []string{"host"}) {
		t.Fatalf("the owner without --box chose %v", got)
	}
	if got := candidates(mustPlan(t, New(f), Selector{Idle: time.Hour, Box: "jury"}, owner())); !slices.Equal(got, []string{"theirs"}) {
		t.Fatalf("the owner with --box jury chose %v", got)
	}
}

func TestABoxCallerSkipsWhatItMayNotKill(t *testing.T) {
	f := newFake()
	f.add("mine", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	f.add("lent", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	box := Caller{Principal: "sandbox:judge@local:box", Box: "judge", MayKill: func(id string) error {
		if id == "lent" {
			return errors.New("kill on lent: refused")
		}
		return nil
	}}
	if got := candidates(mustPlan(t, New(f), Selector{Idle: time.Hour}, box)); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("chose %v", got)
	}
}

func mustPlan(t *testing.T, c *Collector, sel Selector, caller Caller) Plan {
	t.Helper()
	p, err := c.Plan(sel, caller)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAHeldMemberOfTheDeleteSetKeepsTheWholeCandidate(t *testing.T) {
	f := newFake()
	f.add("parent", "", 40*24*time.Hour, 50*24*time.Hour)
	f.add("child", "parent", 40*24*time.Hour, 50*24*time.Hour).hold = "an agent is live"
	c := New(f)
	p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
	if len(p.Candidates) != 0 {
		t.Fatalf("chose %v while a child is live", candidates(p))
	}
	if why := keptWhy(p, "parent"); why != "h-child: an agent is live" {
		t.Fatalf("parent kept for %q", why)
	}
	if _, err := c.Apply(p); err != nil {
		t.Fatal(err)
	}
	if len(f.reaps) != 0 {
		t.Fatalf("reaped %v", f.reaps)
	}
}

func TestAParentTakesItsChosenBranchesAsOneCandidate(t *testing.T) {
	f := newFake()
	f.add("parent", "", 40*24*time.Hour, 50*24*time.Hour)
	f.add("child", "parent", 40*24*time.Hour, 50*24*time.Hour)
	f.add("grandchild", "child", 40*24*time.Hour, 50*24*time.Hour)
	p := mustPlan(t, New(f), Selector{Idle: 24 * time.Hour}, owner())
	if got := candidates(p); !slices.Equal(got, []string{"parent"}) {
		t.Fatalf("chose %v", got)
	}
	if c := p.Candidates[0]; len(c.Set) != 3 || c.Bytes != 300 {
		t.Fatalf("set %v, bytes %d", c.Set, c.Bytes)
	}
}

func TestAParentWhoseBranchIsNotChosenIsKeptAndTheBranchStandsAlone(t *testing.T) {
	f := newFake()
	f.add("parent", "", 40*24*time.Hour, 50*24*time.Hour)
	f.add("recent", "parent", time.Hour, 50*24*time.Hour)
	f.add("stale", "parent", 40*24*time.Hour, 50*24*time.Hour)
	p := mustPlan(t, New(f), Selector{Idle: 24 * time.Hour}, owner())
	if got := candidates(p); !slices.Equal(got, []string{"stale"}) {
		t.Fatalf("chose %v", got)
	}
	if why := keptWhy(p, "parent"); !strings.Contains(why, "h-recent") {
		t.Fatalf("parent kept for %q", why)
	}
}

func TestAnAlreadyReapedBranchDoesNotKeepItsParent(t *testing.T) {
	f := newFake()
	f.add("parent", "", 40*24*time.Hour, 50*24*time.Hour)
	f.add("gone", "parent", time.Hour, 50*24*time.Hour).reaped = true
	if got := candidates(mustPlan(t, New(f), Selector{Idle: 24 * time.Hour}, owner())); !slices.Equal(got, []string{"parent"}) {
		t.Fatalf("chose %v", got)
	}
}

func TestApplyStampsTheRunAndReapsEachGroupOnce(t *testing.T) {
	f := newFake()
	f.add("a", "", 40*24*time.Hour, 50*24*time.Hour)
	f.add("a1", "a", 40*24*time.Hour, 50*24*time.Hour)
	f.add("b", "", 40*24*time.Hour, 50*24*time.Hour)
	c := New(f)
	p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
	res, err := c.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reaped) != 2 || len(f.reaps) != 2 {
		t.Fatalf("reaped %v (%v)", res.Reaped, f.reaps)
	}
	for _, id := range []string{"a", "a1", "b"} {
		if f.stamps[id] != "gc:"+p.Run {
			t.Fatalf("%s stamped %q, want gc:%s", id, f.stamps[id], p.Run)
		}
	}
}

func TestApplyDropsWhatChangedSinceThePlanAndNeverWidens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *fakeWorld)
		why    string
	}{
		{"used since", func(f *fakeWorld) { f.nodes["x"].row.LastUseMS = f.now.UnixMilli() }, "no longer chosen"},
		{"held since", func(f *fakeWorld) { f.nodes["x"].hold = "a shell is bound" }, "kept now: a shell is bound"},
		{"a branch held since", func(f *fakeWorld) {
			f.add("x1", "x", 40*24*time.Hour, 50*24*time.Hour).hold = "an agent is live"
		}, "kept now: h-x1: an agent is live"},
		{"a branch born since", func(f *fakeWorld) { f.add("x1", "x", 40*24*time.Hour, 50*24*time.Hour) }, ErrSetChanged.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.add("x", "", 40*24*time.Hour, 50*24*time.Hour)
			f.add("y", "", 40*24*time.Hour, 50*24*time.Hour)
			c := New(f)
			p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
			f.add("z", "", 40*24*time.Hour, 50*24*time.Hour)
			tc.change(f)
			res, err := c.Apply(p)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.reaps, []string{"y"}) {
				t.Fatalf("reaped %v, want only y", f.reaps)
			}
			if len(res.Dropped) != 1 || res.Dropped[0].ID != "x" || res.Dropped[0].Why != tc.why {
				t.Fatalf("dropped %+v, want x: %s", res.Dropped, tc.why)
			}
		})
	}
}

func TestApplyRefusesASetThatGrewBetweenTheRecheckAndTheReap(t *testing.T) {
	f := newFake()
	f.add("x", "", 40*24*time.Hour, 50*24*time.Hour)
	c := New(f)
	p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
	f.onReap = func(root string) {
		if _, ok := f.nodes["x1"]; !ok {
			f.add("x1", "x", time.Hour, time.Hour)
		}
	}
	res, err := c.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reaped) != 0 || len(res.Dropped) != 1 || f.nodes["x"].reaped || f.nodes["x1"].reaped {
		t.Fatalf("res %+v", res)
	}
}

func TestApplyChecksTheCallerOnEveryNode(t *testing.T) {
	f := newFake()
	f.add("x", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	box := Caller{Principal: "sandbox:judge@local:box", Box: "judge"}
	c := New(f)
	p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, box)
	p.Caller.MayKill = func(string) error { return errors.New("kill refused") }
	res, err := c.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reaps) != 0 || len(res.Dropped) != 1 {
		t.Fatalf("res %+v reaps %v", res, f.reaps)
	}
}

func TestAPlanIsTakenOnceByItsOwnCallerWithinItsLife(t *testing.T) {
	f := newFake()
	f.add("x", "", 40*24*time.Hour, 50*24*time.Hour)
	c := New(f)
	p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
	if _, err := c.Take(p.Run, "user:guest"); !errors.Is(err, ErrNotYourPlan) {
		t.Fatalf("another caller took the plan: %v", err)
	}
	if _, err := c.Take(p.Run, owner().Principal); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Take(p.Run, owner().Principal); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("a plan was taken twice: %v", err)
	}
	old := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
	f.now = f.now.Add(PlanLife + time.Second)
	if _, err := c.Take(old.Run, owner().Principal); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("a stale plan was taken: %v", err)
	}
}

func TestAReapFailureIsReportedAndTheRunGoesOn(t *testing.T) {
	f := newFake()
	f.add("x", "", 40*24*time.Hour, 50*24*time.Hour)
	c := New(f)
	p := mustPlan(t, c, Selector{Idle: 24 * time.Hour}, owner())
	f.reapErr = fmt.Errorf("disk full")
	res, err := c.Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || res.Failed[0].Why != "disk full" {
		t.Fatalf("res %+v", res)
	}
}

func TestUnderNamingNothingIsRefused(t *testing.T) {
	f := newFake()
	if _, err := New(f).Plan(Selector{Under: "nobody"}, owner()); err == nil {
		t.Fatal("a plan under an unknown node")
	}
}

func TestABoxPlanNeverNamesAFigaroOutsideIt(t *testing.T) {
	f := newFake()
	f.add("mine", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	f.add("theirs", "mine", 40*24*time.Hour, 50*24*time.Hour)
	f.add("held", "", 40*24*time.Hour, 50*24*time.Hour).box = "judge"
	f.add("outside", "held", 40*24*time.Hour, 50*24*time.Hour).box = "jury"
	f.nodes["outside"].hold = "an agent is live"
	box := Caller{Principal: "sandbox:judge@local:box", Box: "judge"}
	p := mustPlan(t, New(f), Selector{Idle: time.Hour}, box)
	if len(p.Candidates) != 0 {
		t.Fatalf("chose %v", candidates(p))
	}
	for _, k := range p.Kept {
		if k.By == "theirs" || k.By == "outside" || strings.Contains(k.Why, "h-theirs") || strings.Contains(k.Why, "h-outside") {
			t.Fatalf("a box's plan named a figaro outside it: %+v", k)
		}
	}
	if len(p.Kept) != 2 {
		t.Fatalf("kept %+v", p.Kept)
	}
}
