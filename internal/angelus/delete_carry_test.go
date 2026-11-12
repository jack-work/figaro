package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/hierarchy"
)

// El Inspector XV, caveat 2: F is a fork of X with no scopes of its own,
// narrowed only through X, and drawn under P. Nothing is drawn under X, so
// every delete of X is allowed and detaches F from X's history.
func strandedFork(t *testing.T, extraConfig string) (r grantRig, p, x, f string) {
	t.Helper()
	r = newRigConfigured(t, guardPolicy, extraConfig)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	root, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	p = root.FigaroID
	x = ariasFork(t, r, p)
	f = ariasFork(t, r, x)
	narrow(t, r, x, "read:*")
	if err := setTopology(t, r, asOwner(), map[string]string{f: p}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.backend.Node(f); n.Parent != x || n.Present != p {
		t.Fatalf("control: F is stored under %s and drawn under %s", n.Parent, n.Present)
	}
	if eff := effectiveOf(r, f); eff != "read:*" {
		t.Fatalf("control: F holds %q through X", eff)
	}
	return r, p, x, f
}

func effectiveOf(r grantRig, id string) string {
	eff, _ := r.a.Policies.Effective(id)
	return eff.String()
}

func purgeDue(t *testing.T, r grantRig, ids ...string) {
	t.Helper()
	setReapedUntil(t, r.a, time.Now().Add(-time.Second), ids...)
	r.a.SweepLifetimesNow()
}

type deletePath struct {
	name    string
	config  string
	refused bool
	ops     []hierarchy.Op
	run     func(t *testing.T, r grantRig, x, f string)
}

func requireStillNarrow(t *testing.T, r grantRig, f, when string) {
	t.Helper()
	if eff := effectiveOf(r, f); eff != "read:*" {
		t.Fatalf("%s: F holds %q, want read:* as it held through X", when, eff)
	}
}

var deletePaths = []deletePath{
	{"plain kill --now", "", false, []hierarchy.Op{hierarchy.Kill}, func(t *testing.T, r grantRig, x, _ string) {
		if _, err := killAs(r, rpc.KillRequest{FigaroID: x, Now: true}); err != nil {
			t.Fatal(err)
		}
	}},
	{"kill with a grace, then the purge", "", false, []hierarchy.Op{hierarchy.Reap, hierarchy.Purge}, func(t *testing.T, r grantRig, x, f string) {
		if _, err := killAs(r, rpc.KillRequest{FigaroID: x, GraceMS: time.Hour.Milliseconds()}); err != nil {
			t.Fatal(err)
		}
		requireStillNarrow(t, r, f, "reaped")
		purgeDue(t, r, x)
	}},
	{"a reaped group purged by figaro reaped purge", "", false, []hierarchy.Op{hierarchy.Reap, hierarchy.Kill}, func(t *testing.T, r grantRig, x, f string) {
		if _, err := killAs(r, rpc.KillRequest{FigaroID: x, GraceMS: time.Hour.Milliseconds()}); err != nil {
			t.Fatal(err)
		}
		requireStillNarrow(t, r, f, "reaped")
		if _, err := killAs(r, rpc.KillRequest{FigaroID: x, Recursive: true, Now: true}); err != nil {
			t.Fatal(err)
		}
	}},
	{"the ttl sweep, reaped then purged", "", false, []hierarchy.Op{hierarchy.Reap, hierarchy.Purge}, func(t *testing.T, r grantRig, x, f string) {
		age(t, r.a, x, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		r.a.SweepLifetimesNow()
		if !reapedIn(t, r, x) {
			t.Fatal("the sweep did not reap X")
		}
		requireStillNarrow(t, r, f, "reaped by the sweep")
		purgeDue(t, r, x)
	}},
	{"the ttl sweep with no reaped grace", "[memory]\nreaped_grace_hours = 0\n", false, []hierarchy.Op{hierarchy.Sweep}, func(t *testing.T, r grantRig, x, _ string) {
		age(t, r.a, x, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
		r.a.SweepLifetimesNow()
	}},
	{"gc apply, then the purge", "", false, []hierarchy.Op{hierarchy.Reap, hierarchy.Purge}, func(t *testing.T, r grantRig, x, f string) {
		idleFor(t, r.a, x, 40*24*time.Hour)
		if _, err := r.backend.ApplyForm(x, form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.keepalive": json.RawMessage(`"0"`)}, nil)); err != nil {
			t.Fatal(err)
		}
		r.a.SeedRolesNow()
		out, err := r.call(asOwner(), rpc.MethodGCPlan, rpc.GCPlanRequest{Selector: rpc.GCSelector{Match: x}})
		if err != nil {
			t.Fatal(err)
		}
		plan := out.(rpc.GCPlanResponse)
		if len(plan.Candidates) != 1 || plan.Candidates[0].ID != x {
			t.Fatalf("gc plans %+v, kept %+v; want X alone", plan.Candidates, plan.Kept)
		}
		if _, err := r.call(asOwner(), rpc.MethodGCApply, rpc.GCApplyRequest{Run: plan.Run}); err != nil {
			t.Fatal(err)
		}
		requireStillNarrow(t, r, f, "reaped by gc")
		purgeDue(t, r, x)
	}},
	{"an empty discard, which never strands a fork", "", true, []hierarchy.Op{hierarchy.Discard}, func(t *testing.T, r grantRig, x, _ string) {
		if err := r.hs.RemoveAria(x, false); err == nil || !strings.Contains(err.Error(), "discard takes one leaf") {
			t.Fatalf("discard of X with F stored under it = %v, want refused", err)
		}
	}},
}

func TestEveryDeleteCarriesTheNarrowingOfAForkDrawnElsewhere(t *testing.T) {
	for _, path := range deletePaths {
		t.Run(path.name, func(t *testing.T) {
			r, _, x, f := strandedFork(t, path.config)
			path.run(t, r, x, f)
			if path.refused {
				if absent(r, x) {
					t.Fatal("a refused delete took X")
				}
				requireStillNarrow(t, r, f, "after the refusal")
				return
			}
			if !absent(r, x) {
				t.Fatal("X survived the delete")
			}
			if n, _ := r.backend.Node(f); n.Parent == x {
				t.Fatal("F still reads its history through X; the fixture did not detach it")
			}
			requireStillNarrow(t, r, f, "after the delete")
			if got := scopesOf(t, r, f); got != `["read:*"]` {
				t.Fatalf("F's own scopes are %s, want X's narrowing", got)
			}
		})
	}
}

// A departure ships X's whole fork tree, so F leaves with X and keeps its
// lineage on the receiving node: nothing is detached here.
func TestADepartureTakesTheForkDrawnElsewhereWithIt(t *testing.T) {
	r, _, x, f := strandedFork(t, "")
	c, err := r.backend.Component(x)
	if err != nil {
		t.Fatal(err)
	}
	ids := c.TrunkIDs()
	if !strings.Contains(strings.Join(ids, " "), f) {
		t.Fatalf("X's component %v leaves F behind", ids)
	}
}

// A new delete op fails here until a path above drives it, and so carries.
func TestEveryDeleteOpHasACarryPath(t *testing.T) {
	covered := map[hierarchy.Op]bool{hierarchy.Depart: true}
	for _, p := range deletePaths {
		for _, op := range p.ops {
			covered[op] = true
		}
	}
	for op := hierarchy.Op(1); op.String() != "op?"; op++ {
		if op.Deletes() && !covered[op] {
			t.Errorf("%s deletes, and no test drives it past a fork drawn elsewhere", op)
		}
	}
}

func TestNormalizeCarriesTheNarrowingOfAFigaroItDetaches(t *testing.T) {
	r, _, x, f := strandedFork(t, "")
	if _, err := r.call(asOwner(), rpc.MethodNormalize, rpc.NormalizeRequest{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.backend.Node(f); n.Parent == x {
		t.Fatal("normalize left F under X; the fixture did not detach it")
	}
	requireStillNarrow(t, r, f, "after normalize")
}

func TestADeleteThatCannotReadAnOrphansScopesIsRefused(t *testing.T) {
	r, _, x, f := strandedFork(t, "")
	if _, err := r.backend.ApplyForm(f, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: json.RawMessage(`{"not":"a list"}`)}, nil)); err != nil {
		t.Skipf("the store refuses an unreadable scope at write: %v", err)
	}
	_, err := killAs(r, rpc.KillRequest{FigaroID: x, Now: true})
	if err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("kill of X with F's scopes unreadable = %v, want refused", err)
	}
	if absent(r, x) {
		t.Fatal("a refused delete took X")
	}
	if n, _ := r.backend.Node(f); n.Parent != x {
		t.Fatal("a refused delete detached F")
	}
}
