package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

const leasedOutfit = "[system]\nprovider = \"mock\"\nmodel = \"m\"\ntools = [\"say\"]\n"

type notInForce struct{}

func (notInForce) InForce(string) bool { return false }

// A pool daemon as it stands after lease steps 4 to 6: the outfit pinned,
// the ledger row minting, the leased box in the table.
func mintRig(t *testing.T) (grantRig, string) {
	t.Helper()
	r, box := mintRigWithNoDrawnTree(t)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	return r, box
}

func mintRigWithNoDrawnTree(t *testing.T) (grantRig, string) {
	t.Helper()
	r := newRigConfigured(t, grantPolicy, twoPoolConfig)
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.policy), "outfits", "leased.toml"), []byte(leasedOutfit), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodConfigure](asOwner(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	slots, err := angelus.OpenSlotPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.a.Slots = slots
	r.a.Policies.SetLeases(slots)
	if err := slots.Ledger.Begin("groups", "slot0", "chat:10", 1, brokerHolder); err != nil {
		t.Fatal(err)
	}
	box, err := r.a.Settings.LeaseBox("groups", "slot0", 1)
	if err != nil {
		t.Fatal(err)
	}
	return r, box
}

func (r grantRig) nodeIDs() []string {
	var ids []string
	for _, n := range r.backend.Nodes() {
		ids = append(ids, n.ID)
	}
	slices.Sort(ids)
	return ids
}

func (r grantRig) formOf(t *testing.T, id string) map[string]json.RawMessage {
	t.Helper()
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]json.RawMessage{}
	for k, v := range snap.All() {
		out[k] = v
	}
	return out
}

func (r grantRig) drawnUnder(id string) string {
	up, _ := r.backend.HierarchyEngine().Drawn(id)
	return up
}

// What a lease mints is fixed by the pool and the ledger row: one figaro
// born of the pinned outfit with the two stamped keys and nothing of the
// host, one role held in the same box and pointing at it, both minted by
// the leased box, both in the ledger row, both under the pool's node.
func TestALeaseMintsOneFigaroAndItsRoleInItsBoxUnderItsPoolsNode(t *testing.T) {
	r, box := mintRig(t)
	node, ok := r.hs.PoolNode("groups")
	if !ok {
		t.Fatal("the pool has no node after its config loaded")
	}
	if other, ok := r.hs.PoolNode("others"); !ok || other == node {
		t.Fatalf("each pool has its own node: groups %q, others %q", node, other)
	}
	if m, _ := r.backend.Meta(node); m == nil || m.MintedBy != "service:provisioner@local:pool" {
		t.Fatalf("the pool's node is minted by %+v, want the provisioner", m)
	}
	if _, err := r.h[rpc.MethodConfigure](asOwner(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.hs.PoolNode("groups"); again != node {
		t.Fatalf("a reload minted the pool a second node: %q then %q", node, again)
	}

	figaro, role, err := r.hs.MintLeased(asBroker(), "groups", "chat:10", 1)
	if err != nil {
		t.Fatal(err)
	}
	born := r.formOf(t, figaro)
	for key, want := range map[string]string{
		"system.sandbox":     `"` + box + `"`,
		"system.scopes":      `["say:lease:self"]`,
		"system.provider":    `"mock"`,
		"system.tools":       `["say"]`,
		"system.outfit_name": `"leased"`,
		"aria_id":            `"` + figaro + `"`,
	} {
		if got := string(born[key]); got != want {
			t.Errorf("the leased figaro's %s is %s, want %s", key, got, want)
		}
	}
	for key := range born {
		if key == "system.cwd" || strings.HasPrefix(key, "system.environment") || strings.HasPrefix(key, "system.retirement") {
			t.Errorf("the leased figaro's form carries %s", key)
		}
	}
	held := r.formOf(t, role)
	if len(held) != 2 || string(held["system.sandbox"]) != `"`+box+`"` || string(held["target-aria"]) != `"`+figaro+`"` {
		t.Errorf("the role's form is %v, want the box and the figaro and nothing else", held)
	}
	for name, id := range map[string]string{"figaro": figaro, "role": role} {
		if m, _ := r.backend.Meta(id); m == nil || m.MintedBy != "sandbox:"+box+"@local:box" {
			t.Errorf("the %s is minted by %+v, want the leased box", name, m)
		}
		if got := r.drawnUnder(id); got != node {
			t.Errorf("the %s is drawn under %q, want the pool's node %q", name, got, node)
		}
	}
	if n, ok := r.backend.Node(figaro); !ok || n.Kind != store.KindConversation {
		t.Errorf("the leased figaro is a %+v, want a conversation", n)
	}
	row, _ := r.a.Slots.Ledger.Find("groups", "chat:10", 1)
	if !slices.Equal(row.Minted, []string{figaro, role}) {
		t.Errorf("the ledger row names %v, want the figaro then the role", row.Minted)
	}
	if err := r.note(t, asProvisionerServing(box), figaro); err != nil {
		t.Errorf("the provisioner serving the lease cannot write the figaro it minted: %v", err)
	}
}

// Ruling (b). The mint asks the policy for create and use on the box of
// the form it bears, with the grant one lease computes. With no lease in
// force behind the box that decision is no, and nothing is born. Nor is
// anything born for a row that is releasing, or with no row at all.
func TestTheMintBearsNothingThePolicyDoesNotGrantItsLease(t *testing.T) {
	r, _ := mintRig(t)
	before := r.nodeIDs()
	unborn := func(why string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: the mint bore a figaro", why)
		}
		if after := r.nodeIDs(); !slices.Equal(after, before) {
			t.Errorf("%s: the store went from %v to %v", why, before, after)
		}
		if row, _ := r.a.Slots.Ledger.Find("groups", "chat:10", 1); len(row.Minted) != 0 {
			t.Errorf("%s: the ledger row names %v", why, row.Minted)
		}
	}

	r.a.Policies.SetLeases(notInForce{})
	_, _, err := r.hs.MintLeased(asBroker(), "groups", "chat:10", 1)
	unborn("no lease in force", err)
	if err != nil && !strings.Contains(err.Error(), "create on sandbox:slot0-g1") {
		t.Errorf("the refusal is not the policy's answer on create: %v", err)
	}
	r.a.Policies.SetLeases(r.a.Slots)

	_, _, err = r.hs.MintLeased(asBroker(), "groups", "chat:11", 1)
	unborn("a key with no row", err)
	_, _, err = r.hs.MintLeased(asBroker(), "others", "chat:10", 1)
	unborn("the row's key in another pool", err)

	if _, err := r.a.Slots.Ledger.Release("groups", "chat:10", 1); err != nil {
		t.Fatal(err)
	}
	_, _, err = r.hs.MintLeased(asBroker(), "groups", "chat:10", 1)
	unborn("a releasing lease", err)
}

// Ruling (d). The provisioner places through the topology's guard, which
// asks whether the node was minted by the mint now under way for the
// lease, and whether the target is that lease's pool's node. Outside a
// mint it places nothing, a figaro of the same box that this mint did not
// bear stays where it is, and no node goes under another pool's node or a
// figaro.
func TestTheProvisionerPlacesOnlyWhatItsMintJustBoreAndOnlyUnderItsPool(t *testing.T) {
	r, box := mintRig(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.policy), "outfits", "boxed.toml"), []byte("[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \""+box+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inBox := func() string {
		t.Helper()
		resp, err := r.create(asOwner(), "boxed")
		if err != nil {
			t.Fatal(err)
		}
		return resp.FigaroID
	}
	fresh, older, target := inBox(), inBox(), inBox()
	groups, _ := r.hs.PoolNode("groups")
	others, _ := r.hs.PoolNode("others")
	serving := asProvisionerServing(box)
	stays := func(why, id, under string) {
		t.Helper()
		was := r.drawnUnder(id)
		if err := r.hs.Place(serving, id, under); err == nil {
			t.Errorf("%s: the provisioner placed %s under %s", why, id, under)
		}
		if got := r.drawnUnder(id); got != was {
			t.Errorf("%s: the node moved from under %q to under %q", why, was, got)
		}
	}

	stays("no mint under way", fresh, groups)
	minted, done, err := r.a.Slots.BeginMint(box)
	if err != nil {
		t.Fatal(err)
	}
	minted(fresh)
	stays("a figaro of the box this mint did not bear", older, groups)
	stays("under another pool's node", fresh, others)
	stays("under a figaro of its own box", fresh, target)
	if err := r.hs.Place(asProvisionerServing("slot0-g2"), fresh, groups); err == nil {
		t.Error("serving the slot's next generation the provisioner placed this lease's figaro")
	}
	if err := r.hs.Place(serving, fresh, groups); err != nil {
		t.Fatalf("the placement the mint makes was refused: %v", err)
	}
	if got := r.drawnUnder(fresh); got != groups {
		t.Errorf("the placed figaro is drawn under %q, want the pool's node %q", got, groups)
	}
	done()
	stays("the mint is over", older, groups)
}

// A lease that cannot be placed under its pool's node is not minted: left
// where the store put it, the figaro would hang under its outfit and take
// whatever a grant on that lineage gives. A pool daemon's config refuses
// trunks = false; this is the mint's own refusal, and nothing of the box
// is left behind.
func TestWithNoDrawnTreeNothingIsMinted(t *testing.T) {
	r, box := mintRigWithNoDrawnTree(t)
	figaro, role, err := r.hs.MintLeased(asBroker(), "groups", "chat:10", 1)
	if err == nil {
		t.Fatalf("with no tree to place into the mint bore %s and %s", figaro, role)
	}
	for _, n := range r.backend.Nodes() {
		if n.Kind != store.KindForm && n.Kind != store.KindConversation {
			continue
		}
		if snap, err := r.backend.FormState(n.ID); err == nil {
			if in := snap.Lookup("system.sandbox"); in != nil && *in == box {
				t.Errorf("the refused mint left %s (%s) in the box", n.ID, n.Kind)
			}
		}
	}
}
