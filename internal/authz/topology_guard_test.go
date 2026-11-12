package authz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/internal/hierarchy"
)

var guardOwner = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}

func topologyGuard(t *testing.T, policy string) TopologyGuard {
	t.Helper()
	p := NewPolicies(guardOwner, nil)
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	return TopologyGuard{
		Policies: func() *Policies { return p },
		Owner:    func() jkauthz.Principal { return guardOwner },
		Placement: func(id string) (string, int64) {
			if strings.HasPrefix(id, "box-") {
				return "judge", 0
			}
			return "local", 0
		},
	}
}

func guardCaller(kind jkauthz.Kind, id string, door Door) hierarchy.Principal {
	p := jkauthz.Principal{Kind: kind, ID: id}
	return hierarchy.Principal{Name: p.String(), Subject: TopologyCaller{Subject: jkauthz.Subject{Principal: p}, Door: door}}
}

const setOnlyPolicy = `
[roles.owner]
grants = ["*:*"]

[roles.setter]
grants = ["set:*"]

[[bind]]
role = "owner"
principals = ["user:1000"]

[[bind]]
role = "setter"
principals = ["user:mallory"]
`

// Engineer VI, ask 3: a storage move changes the drawn position too, so set
// without kill is refused outright and never reaches the drawn-only downgrade.
func TestSetWithoutKillOnAStorageMoveIsRefusedNotDowngraded(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	p := hierarchy.Plan{Op: hierarchy.Arrange, Moves: []hierarchy.Move{{Node: "a", From: "s", To: "b", Tree: hierarchy.Placement}}}
	_, err := g.Check(context.Background(), guardCaller(jkauthz.KindUser, "mallory", DoorLocal), p)
	if err == nil || errors.Is(err, hierarchy.ErrDrawnOnly) {
		t.Fatalf("set-only storage move = %v, want a refusal that is not ErrDrawnOnly", err)
	}
}

func TestTheOwnersStorageMoveUnderAForeignMinterIsDrawnOnly(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	owner := guardCaller(guardOwner.Kind, guardOwner.ID, DoorLocal)
	mine := hierarchy.Move{Node: "a", From: "s", To: "b", Tree: hierarchy.Placement, NodeOwner: guardOwner.String()}
	if _, err := g.Check(context.Background(), owner, hierarchy.Plan{Op: hierarchy.Arrange, Moves: []hierarchy.Move{mine}}); err != nil {
		t.Fatalf("the owner's storage move between his own arias: %v", err)
	}
	foreign := mine
	foreign.ToOwner = "sandbox:judge"
	_, err := g.Check(context.Background(), owner, hierarchy.Plan{Op: hierarchy.Arrange, Moves: []hierarchy.Move{foreign}})
	if !errors.Is(err, hierarchy.ErrDrawnOnly) {
		t.Fatalf("the owner's storage move under a box-minted parent = %v, want ErrDrawnOnly", err)
	}
}

// Engineer VI, ask 4: a re-home handed in under another sandbox is refused
// by the guard even though Apply would never derive it.
func TestARehomeIntoAnotherSandboxIsRefused(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	p := hierarchy.Plan{Op: hierarchy.Purge, Node: "z", Delete: []string{"z"},
		Moves: []hierarchy.Move{{Node: "o", From: "z", To: "box-1", Tree: hierarchy.Drawn, Rehome: true}}}
	if _, err := g.Check(context.Background(), hierarchy.System, p); err == nil {
		t.Fatal("a re-home under a node in another sandbox passed the guard")
	}
}

func TestTheSystemPrincipalOnlyRehomesAndSweeps(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	p := hierarchy.Plan{Op: hierarchy.Promote, Moves: []hierarchy.Move{{Node: "a", From: "p", To: "g", Tree: hierarchy.Drawn}}}
	if _, err := g.Check(context.Background(), hierarchy.System, p); err == nil {
		t.Fatal("the system principal promoted")
	}
	p = hierarchy.Plan{Op: hierarchy.Purge, Node: "z", Delete: []string{"z"},
		Moves: []hierarchy.Move{{Node: "o", From: "z", To: "p", Tree: hierarchy.Placement, Rehome: true}}}
	if _, err := g.Check(context.Background(), hierarchy.System, p); err == nil {
		t.Fatal("the system principal re-homed in storage")
	}
}

func TestOnlyTheSystemSweeps(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	owner := guardCaller(guardOwner.Kind, guardOwner.ID, DoorLocal)
	_, err := g.Check(context.Background(), owner, hierarchy.Plan{Op: hierarchy.Sweep, Node: "z", Delete: []string{"z"}})
	if !errors.Is(err, ErrSystemOnly) {
		t.Fatalf("the owner sweeping = %v, want ErrSystemOnly", err)
	}
}

func TestASweepAcrossSandboxesIsHeld(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	_, err := g.Check(context.Background(), hierarchy.System, hierarchy.Plan{Op: hierarchy.Sweep, Node: "z", Delete: []string{"z", "box-1"}})
	var held *HeldError
	if !errors.As(err, &held) || held.Node != "box-1" {
		t.Fatalf("a sweep taking a node in another sandbox = %v, want held on box-1", err)
	}
}

// El Inspector XIV, C2: the daemon purges only a reaped root past its grace,
// and only the nodes reaped with it.
func TestAPurgeTakesOnlyAReapedGroupPastItsGrace(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	g.Reaped = func(_ context.Context, root string) ([]string, bool) {
		switch root {
		case "due":
			return []string{"due", "kid"}, true
		case "young":
			return []string{"young"}, false
		}
		return nil, false
	}
	purge := func(root string, del ...string) error {
		_, err := g.Check(context.Background(), hierarchy.System, hierarchy.Plan{Op: hierarchy.Purge, Node: root, Delete: del})
		return err
	}
	if err := purge("live", "live"); err == nil {
		t.Fatal("the daemon purged a live, unreaped node")
	}
	if err := purge("young", "young"); err == nil {
		t.Fatal("the daemon purged a reaped node inside its grace")
	}
	if err := purge("due", "due", "kid", "stranger"); err == nil {
		t.Fatal("the daemon purged a node drawn under a reaped root after the reap")
	}
	if err := purge("due", "due", "kid"); err != nil {
		t.Fatalf("purge of a reaped group past its grace: %v", err)
	}
}

func TestADiscardTakesOneLeafWithNoMessagesOfItsOwn(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	g.Empty = func(id string) bool { return id == "fresh" }
	discard := func(p hierarchy.Plan) error {
		p.Op = hierarchy.Discard
		_, err := g.Check(context.Background(), hierarchy.System, p)
		return err
	}
	if err := discard(hierarchy.Plan{Node: "fresh", Delete: []string{"fresh"}}); err != nil {
		t.Fatalf("discard of a fresh leaf: %v", err)
	}
	if err := discard(hierarchy.Plan{Node: "spoken", Delete: []string{"spoken"}}); err == nil {
		t.Fatal("the daemon discarded an aria with messages of its own")
	}
	if err := discard(hierarchy.Plan{Node: "fresh", Delete: []string{"fresh", "kid"}}); err == nil {
		t.Fatal("the daemon discarded a node with a child")
	}
}

func TestOnlyTheSystemDeparts(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	owner := guardCaller(guardOwner.Kind, guardOwner.ID, DoorLocal)
	if _, err := g.Check(context.Background(), owner, hierarchy.Plan{Op: hierarchy.Depart, Node: "a", Delete: []string{"a"}}); !errors.Is(err, ErrSystemOnly) {
		t.Fatalf("the owner departing = %v, want ErrSystemOnly", err)
	}
}

// D1 (1): no delete takes a reaped root without the rest of its group, the
// daemon's purge included.
func TestADeleteNeverSplitsAReapedGroup(t *testing.T) {
	g := topologyGuard(t, setOnlyPolicy)
	g.Reaped = func(_ context.Context, root string) ([]string, bool) {
		if root == "p" {
			return []string{"p", "c"}, true
		}
		return nil, false
	}
	owner := guardCaller(guardOwner.Kind, guardOwner.ID, DoorLocal)
	for _, by := range []hierarchy.Principal{owner, hierarchy.System} {
		op := hierarchy.Kill
		if by.System {
			op = hierarchy.Purge
		}
		_, err := g.Check(context.Background(), by, hierarchy.Plan{Op: op, Node: "p", Delete: []string{"p"}})
		if !errors.Is(err, ErrSplitsReapedGroup) {
			t.Errorf("%s of p without c = %v, want ErrSplitsReapedGroup", op, err)
		}
		if _, err := g.Check(context.Background(), by, hierarchy.Plan{Op: op, Node: "p", Delete: []string{"p", "c"}}); err != nil {
			t.Errorf("%s of the whole group: %v", op, err)
		}
	}
}
