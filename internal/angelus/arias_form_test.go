package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func readArias(t *testing.T, r grantRig, ctx context.Context, id string) (form.Snapshot, error) {
	t.Helper()
	out, err := r.h[rpc.MethodForm](ctx, withAria(t, id, rpc.FormRequest{}))
	if err != nil {
		return form.Snapshot{}, err
	}
	switch resp := out.(type) {
	case rpc.FormResponse:
		return resp.Snapshot, nil
	case *rpc.FormResponse:
		return resp.Snapshot, nil
	}
	t.Fatalf("form read answered %T", out)
	return form.Snapshot{}, nil
}

func ariaIDsIn(snap form.Snapshot) string {
	var ids []string
	for k, v := range snap.All() {
		if strings.HasSuffix(k, ".id") {
			ids = append(ids, strings.Trim(string(v), `"`))
		}
	}
	return strings.Join(ids, ",")
}

// @arias is computed for each caller from the arias that caller may read: one
// principal never sees another's rows, and there is no per-principal id on the
// wire to name instead. Naming anything else under @arias is not found.
func TestEachCallerReadsTheirOwnArias(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	owned, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	remote := as("user", "guest")
	theirs, err := r.create(remote, "mock")
	if err != nil {
		t.Fatal(err)
	}

	ownerView, err := readArias(t, r, asOwner(), rpc.AriasFormID)
	if err != nil {
		t.Fatal(err)
	}
	if ids := ariaIDsIn(ownerView); !strings.Contains(ids, owned.FigaroID) {
		t.Errorf("the owner's @arias = %s, want its own %s", ids, owned.FigaroID)
	}
	if _, err := r.call(remote, rpc.MethodAttach, rpc.AttachRequest{FigaroID: rpc.AriasFormID}); err != nil {
		t.Errorf("a principal holding a role cannot attach @arias: %v", err)
	}
	remoteView, err := readArias(t, r, remote, rpc.AriasFormID)
	if err != nil {
		t.Fatalf("a principal holding a role cannot read its @arias: %v", err)
	}
	ids := ariaIDsIn(remoteView)
	if strings.Contains(ids, owned.FigaroID) {
		t.Errorf("the guest's @arias shows the owner's aria: %s", ids)
	}
	if !strings.Contains(ids, theirs.FigaroID) {
		t.Errorf("the guest's @arias = %s, want its own %s", ids, theirs.FigaroID)
	}

	for _, guess := range []string{
		"@arias/user:SELF", "@arias~0", "@arias.", "@arias@user:guest", "@ariasx",
		"@arias\x00user:guest",
	} {
		if _, err := readArias(t, r, remote, guess); err == nil {
			t.Errorf("reading %q was answered: only @arias names the caller's own", guess)
		}
	}
	set := form.Build(form.Snapshot{}, map[string]json.RawMessage{"peers": json.RawMessage("1")}, nil)
	if _, err := r.h[rpc.MethodSet](remote, withAria(t, rpc.AriasFormID, rpc.SetRequest{Patch: set})); err == nil {
		t.Error("@arias took a write")
	}
}

// A principal with no role here reads nothing, as with list.
func TestAStrangerCannotReadArias(t *testing.T) {
	r := newGrantRig(t)
	if _, err := r.create(asOwner(), "mock"); err != nil {
		t.Fatal(err)
	}
	if _, err := readArias(t, r, as("user", "stranger"), rpc.AriasFormID); err == nil {
		t.Error("a principal holding no role read @arias")
	}
}

// A fork nests under its parent, so the form's nesting is the fork tree.
func TestAForkNestsUnderItsParent(t *testing.T) {
	r := newGrantRig(t)
	parent, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: parent.FigaroID})
	if err != nil {
		t.Fatal(err)
	}
	child := out.(rpc.ForkResponse).Alternative
	snap, err := readArias(t, r, asOwner(), rpc.AriasFormID)
	if err != nil {
		t.Fatal(err)
	}
	var parentPath, childPath string
	for k, v := range snap.All() {
		switch strings.Trim(string(v), `"`) {
		case parent.FigaroID:
			parentPath = strings.TrimSuffix(k, ".id")
		case child:
			childPath = strings.TrimSuffix(k, ".id")
		}
	}
	if parentPath == "" || !strings.HasPrefix(childPath, parentPath+".forks.") {
		t.Errorf("child at %q, parent at %q: want the child under the parent's forks", childPath, parentPath)
	}
}

// A read of @arias that does not follow it still sees the list as it is now,
// not as the first read built it.
func TestAReadOfAriasIsCurrent(t *testing.T) {
	r := newGrantRig(t)
	first, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := readArias(t, r, asOwner(), rpc.AriasFormID); !strings.Contains(ariaIDsIn(snap), first.FigaroID) {
		t.Fatal("the first read does not list the aria")
	}
	second, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := readArias(t, r, asOwner(), rpc.AriasFormID); !strings.Contains(ariaIDsIn(snap), second.FigaroID) {
		t.Error("a later read does not list an aria made since the first")
	}
}

func ariasPaths(t *testing.T, r grantRig) map[string]string {
	t.Helper()
	snap, err := readArias(t, r, asOwner(), rpc.AriasFormID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range snap.All() {
		if strings.HasSuffix(k, ".id") {
			out[strings.Trim(string(v), `"`)] = strings.TrimSuffix(k, ".id")
		}
	}
	return out
}

func ariasFork(t *testing.T, r grantRig, id string) string {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: id})
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.ForkResponse).Alternative
}

// matchesPresentation checks every listed aria sits, in @arias, directly
// under the parent the store presents for it (figaro ls's drawnUnder), or at
// the top when that parent is not an aria.
func matchesPresentation(t *testing.T, r grantRig, ids ...string) {
	t.Helper()
	paths := ariasPaths(t, r)
	for _, id := range ids {
		n, ok := r.backend.Node(id)
		if !ok {
			continue
		}
		up := n.Present
		if up == "" {
			up = n.Parent
		}
		got := paths[id]
		if got == "" {
			t.Errorf("%s is missing from @arias", id)
			continue
		}
		want, listed := paths[up]
		if !listed {
			if strings.Contains(got, ".forks.") {
				t.Errorf("%s is drawn at %s; the store presents it at the top", id, got)
			}
			continue
		}
		if i := strings.LastIndex(got, ".forks."); i < 0 || got[:i] != want {
			t.Errorf("%s is drawn at %s; the store presents it under %s (%s)", id, got, up, want)
		}
	}
}

func presentedUnder(t *testing.T, r grantRig, id string) string {
	t.Helper()
	n, _ := r.backend.Node(id)
	return n.Present
}

// @arias draws the tree the topology presents, as figaro ls does: a promote,
// a demote and the rehoming that follows a delete move a row, while the
// history underneath stays where it was.
func TestAriasFollowsThePresentedTopology(t *testing.T) {
	r := newGrantRig(t)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	root, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	a := root.FigaroID
	b := ariasFork(t, r, a)
	c := ariasFork(t, r, b)
	d := ariasFork(t, r, c)
	e := ariasFork(t, r, d)
	all := []string{a, b, c, d, e}
	matchesPresentation(t, r, all...)

	moved := func(t *testing.T, id, before string) {
		t.Helper()
		if presentedUnder(t, r, id) == before {
			t.Fatalf("the operation left %s where it was: the case tests nothing", id)
		}
	}
	t.Run("promote one level", func(t *testing.T) {
		before := presentedUnder(t, r, c)
		if _, err := r.call(asOwner(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: c, Levels: 1}); err != nil {
			t.Fatal(err)
		}
		moved(t, c, before)
		matchesPresentation(t, r, all...)
		snap, err := readArias(t, r, asOwner(), rpc.AriasFormID)
		if err != nil {
			t.Fatal(err)
		}
		at := ariasPaths(t, r)[c]
		if got, _ := snap.Get(at + ".parent"); string(got) != `"`+b+`"` {
			t.Errorf("%s.parent = %s, want its history parent %s", at, got, b)
		}
		over, err := readArias(t, r, asOwner(), rpc.TopologyFormID)
		if err != nil {
			t.Fatal(err)
		}
		if got := over.Lookup(c); got == nil || *got != presentedUnder(t, r, c) {
			t.Errorf("@topology %s = %v, want the aria it is drawn under, %s", c, got, presentedUnder(t, r, c))
		}
		if got, ok := over.Get(a); ok {
			t.Errorf("an aria where its history puts it is overridden: %s", got)
		}
	})
	t.Run("promote two levels", func(t *testing.T) {
		before := presentedUnder(t, r, e)
		if _, err := r.call(asOwner(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: e, Levels: 2}); err != nil {
			t.Fatal(err)
		}
		moved(t, e, before)
		matchesPresentation(t, r, all...)
	})
	t.Run("demote under another", func(t *testing.T) {
		before := presentedUnder(t, r, e)
		out, err := r.call(asOwner(), rpc.MethodDemote, rpc.DemoteRequest{FigaroID: e})
		if err != nil {
			t.Fatal(err)
		}
		if out.(rpc.DemoteResponse).Under == "" {
			t.Fatal("the demote had nowhere to go")
		}
		moved(t, e, before)
		matchesPresentation(t, r, all...)
	})
	t.Run("rehomed when what it was drawn under is deleted", func(t *testing.T) {
		f := ariasFork(t, r, a)
		g := ariasFork(t, r, f)
		if _, err := r.call(asOwner(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: g, Levels: 1}); err != nil {
			t.Fatal(err)
		}
		if up := presentedUnder(t, r, f); up != g {
			t.Fatalf("after the promote %s is presented under %q, want %s", f, up, g)
		}
		if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: g, Now: true, Recursive: true}); err != nil {
			t.Fatal(err)
		}
		paths := ariasPaths(t, r)
		if _, still := paths[g]; still {
			t.Fatalf("%s is still listed after kill --now", g)
		}
		if _, kept := paths[f]; !kept {
			t.Fatalf("%s went with %s; nothing was rehomed", f, g)
		}
		matchesPresentation(t, r, append(all, f)...)
	})
	t.Run("promote to the top", func(t *testing.T) {
		before := presentedUnder(t, r, b)
		if _, err := r.call(asOwner(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: b, Levels: 9}); err != nil {
			t.Fatal(err)
		}
		moved(t, b, before)
		matchesPresentation(t, r, all...)
	})
}

func setTopology(t *testing.T, r grantRig, ctx context.Context, set map[string]string, unset ...string) error {
	t.Helper()
	keys := map[string]json.RawMessage{}
	for k, v := range set {
		keys[k], _ = json.Marshal(v)
	}
	req := rpc.SetRequest{Patch: form.Build(form.Snapshot{}, keys, unset), Assert: len(unset) > 0}
	_, err := r.h[rpc.MethodSet](ctx, withAria(t, rpc.TopologyFormID, req))
	return err
}

// @topology is the override half of @arias: a key per aria drawn away from
// its history, valued with the aria it is drawn under. Writing it moves the
// row; removing a key sends the aria back under its history parent.
func TestTopologyIsWrittenThroughItsOwnForm(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	root, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	a := root.FigaroID
	b := ariasFork(t, r, a)
	c := ariasFork(t, r, b)
	other, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	o := other.FigaroID

	if err := setTopology(t, r, asOwner(), map[string]string{c: o}); err != nil {
		t.Fatal(err)
	}
	if up := presentedUnder(t, r, c); up != o {
		t.Fatalf("after the set %s is presented under %q, want %s", c, up, o)
	}
	matchesPresentation(t, r, a, b, c, o)
	over, err := readArias(t, r, asOwner(), rpc.TopologyFormID)
	if err != nil {
		t.Fatal(err)
	}
	if got := over.Lookup(c); got == nil || *got != o {
		t.Errorf("@topology %s = %v, want %s", c, got, o)
	}

	if err := setTopology(t, r, asOwner(), map[string]string{c: ""}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ariasPaths(t, r)[c], "arias.") || strings.Contains(ariasPaths(t, r)[c], ".forks.") {
		t.Errorf("an aria set to \"\" is drawn at %s, not the top", ariasPaths(t, r)[c])
	}

	if err := setTopology(t, r, asOwner(), map[string]string{a: b}); err == nil {
		t.Error("an aria was drawn under its own descendant")
	}
	if err := setTopology(t, r, asOwner(), nil, c); err != nil {
		t.Fatal(err)
	}
	if up := presentedUnder(t, r, c); up != b {
		t.Errorf("after the unset %s is presented under %q, want its history parent %s", c, up, b)
	}
	over, _ = readArias(t, r, asOwner(), rpc.TopologyFormID)
	if got, ok := over.Get(c); ok {
		t.Errorf("@topology still holds %s = %s after the unset", c, got)
	}
	if err := setTopology(t, r, asOwner(), nil, c); err == nil {
		t.Error("unsetting an aria drawn where its history puts it was not refused")
	}

	if _, err := r.call(asOwner(), rpc.MethodPromote, rpc.PromoteRequest{FigaroID: c, Levels: 1}); err != nil {
		t.Fatal(err)
	}
	if up := presentedUnder(t, r, b); up != c {
		t.Fatalf("after the promote %s is presented under %q, want %s", b, up, c)
	}
	if err := setTopology(t, r, asOwner(), nil, c); err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, r, asOwner(), map[string]string{c: o}); err != nil {
		t.Fatal(err)
	}
	if up := presentedUnder(t, r, b); up != a {
		t.Errorf("clearing half a promote left the other half behind: %s followed %s under %q", b, c, up)
	}
	if err := setTopology(t, r, asOwner(), nil, c); err != nil {
		t.Fatal(err)
	}

	guest := as("user", "guest")
	theirs, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := setTopology(t, r, guest, map[string]string{c: theirs.FigaroID}); err == nil {
		t.Error("a guest moved the owner's aria")
	}
	if err := setTopology(t, r, guest, map[string]string{theirs.FigaroID: a}); err == nil {
		t.Error("a guest drew its aria under one it cannot read")
	}
	if up := presentedUnder(t, r, c); up != b {
		t.Errorf("a refused write moved %s under %q", c, up)
	}
	view, err := readArias(t, r, guest, rpc.TopologyFormID)
	if err != nil {
		t.Fatalf("a guest holding a role cannot read @topology: %v", err)
	}
	for k := range view.All() {
		if k != theirs.FigaroID {
			t.Errorf("the guest's @topology names %s, an aria it cannot read", k)
		}
	}
}

func lend(verbs []string, ids ...string) string {
	var grants []string
	for _, v := range verbs {
		for _, id := range ids {
			grants = append(grants, `"`+v+`:aria:`+id+`"`)
		}
	}
	return strings.Join(grants, ", ")
}

func lendGuest(t *testing.T, r grantRig, grants ...string) {
	t.Helper()
	text := doorPolicy + "\n[roles.lent]\ngrants = [" + strings.Join(grants, ", ") + "]\n\n[[bind]]\nrole = \"lent\"\nprincipals = [\"user:guest\"]\n"
	if err := os.WriteFile(r.policy, []byte(strings.ReplaceAll(text, "SELF", strconv.Itoa(os.Getuid()))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
}

// Drawing an aria under P changes how P's subtree is drawn, which is a write
// to P: read on P is not enough, or a guest lent read could hang an aria of
// its own, with a mantra of its choosing, among the owner's children.
func TestArrangingUnderAnAriaNeedsSetOnIt(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	owners, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	guest := as("user", "guest")
	theirs, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	p, g := owners.FigaroID, theirs.FigaroID

	lendGuest(t, r, lend([]string{"read"}, p))
	err = setTopology(t, r, guest, map[string]string{g: p})
	if err == nil {
		t.Fatal("a guest lent only read drew its aria under the owner's")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("the refusal tells a readable aria apart from a missing one: %v", err)
	}
	if up := presentedUnder(t, r, g); up == p {
		t.Error("a refused write moved the guest's aria under the owner's")
	}

	lendGuest(t, r, lend([]string{"read", "set"}, p), lend([]string{"kill"}, g))
	if err := setTopology(t, r, guest, map[string]string{g: p}); err != nil {
		t.Fatalf("a guest lent set on the owner's aria cannot draw under it: %v", err)
	}
}

// Restoring an aria can put another aria's override on a cycle, and dropping
// that override moves the other aria: a caller without kill on it is refused
// the whole edit (the kill floor).
func TestAnArrangeDoesNotUndoAnOverrideTheCallerCannotKill(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	root, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	y, z := root.FigaroID, other.FigaroID
	x := ariasFork(t, r, y)
	if err := setTopology(t, r, asOwner(), map[string]string{x: z, y: x}); err != nil {
		t.Fatal(err)
	}
	if presentedUnder(t, r, y) != x || presentedUnder(t, r, x) != z {
		t.Fatalf("the owner's arrangement did not land: %s under %q, %s under %q", y, presentedUnder(t, r, y), x, presentedUnder(t, r, x))
	}

	guest := as("user", "guest")
	lendGuest(t, r, lend([]string{"read", "set", "kill"}, x), lend([]string{"read", "set"}, y), lend([]string{"read"}, z))
	err = setTopology(t, r, guest, nil, x)
	if err == nil {
		t.Error("a guest with kill on x only undid the owner's override on y")
	} else if !strings.Contains(err.Error(), "kill on aria:"+y) {
		t.Errorf("the restore was refused, but not for the override on y: %v", err)
	}
	if presentedUnder(t, r, y) != x || presentedUnder(t, r, x) != z {
		t.Errorf("a refused restore changed the drawing: %s under %q, %s under %q", y, presentedUnder(t, r, y), x, presentedUnder(t, r, x))
	}

	lendGuest(t, r, lend([]string{"read", "set", "kill"}, x, y), lend([]string{"read"}, z))
	if err := setTopology(t, r, guest, nil, x); err != nil {
		t.Fatalf("a guest with set and kill on both cannot restore x: %v", err)
	}
	if presentedUnder(t, r, x) != y || presentedUnder(t, r, y) == x {
		t.Errorf("after the restore %s under %q, %s under %q", x, presentedUnder(t, r, x), y, presentedUnder(t, r, y))
	}
}
