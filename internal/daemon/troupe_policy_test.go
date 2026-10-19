package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/jkauthz"
)

var ana = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "ana@auth.kelliher.info"}

func lendingEverything() string {
	return fmt.Sprintf(`
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "owner"
principals = ["user:%d", "troupe:estate"]
`, os.Getuid())
}

const anaHoldsEverything = `
[roles.all]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "all"
principals = ["user:ana@auth.kelliher.info"]
`

func callAs(t *testing.T, n *ledgerNode, who jkauthz.Principal, method string, req any) (any, error) {
	t.Helper()
	params, err := json.Marshal(req)
	require.NoError(t, err)
	ctx := authz.WithSubject(context.Background(), jkauthz.Subject{Principal: who})
	return n.a.Handlers[method](ctx, params)
}

func owner(n *ledgerNode) jkauthz.Principal { return n.a.Peercred.Self() }

// rootStartsPolicy merges the root-signed version 1 on n, naming setters.
func rootStartsPolicy(t *testing.T, n *ledgerNode, root estateRoot, body string, setters ...string) {
	t.Helper()
	list, _ := json.Marshal(setters)
	e, err := troupe.SignRootPolicy(root.key, "estate", 1, "root:estate", "setters = "+string(list)+"\n"+body, time.Now())
	require.NoError(t, err)
	_, err = callAs(t, n, owner(n), rpc.MethodTroupeEntries, rpc.TroupeEntriesRequest{Troupe: "estate", Entries: troupe.Encode([]troupe.Entry{e})})
	require.NoError(t, err)
}

func policyEntries(t *testing.T, n *ledgerNode) int {
	t.Helper()
	count := 0
	for _, raw := range storedEntries(t, n) {
		var e troupe.Entry
		require.NoError(t, json.Unmarshal([]byte(raw), &e))
		if e.Kind == troupe.Policy {
			count++
		}
	}
	return count
}

// The root starts the policy; a setter's later version is signed, in force
// there, spread to every member before the call returns, and in force on
// them too. Every version stays in the stored ledger, so each can be
// checked against the one before it.
func TestATroupePolicySetOnOneNodeIsInForceOnEveryMember(t *testing.T) {
	na, nb, ma, mb, root := pairWith(t, lendingEverything())
	_, err := callAs(t, na, owner(na), rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: anaHoldsEverything})
	require.ErrorContains(t, err, "the root signs the first")
	rootStartsPolicy(t, na, root, "", na.id())
	out, err := callAs(t, na, owner(na), rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: "setters = [\"" + na.id() + "\"]\n" + anaHoldsEverything})
	require.NoError(t, err)
	resp := out.(rpc.TroupePolicySetResponse)
	require.Equal(t, uint64(2), resp.Version)
	require.Equal(t, []string{nb.id()}, resp.Reached, "%+v", resp.Unreachable)
	require.Equal(t, uint64(2), mb.PolicyVersion())
	for _, n := range []*ledgerNode{na, nb} {
		require.True(t, n.a.Policies.Allow(jkauthz.Subject{Principal: ana}, rpc.VerbSend, authz.Aria("aaaa0001")).Allowed)
	}

	_, err = callAs(t, na, owner(na), rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: "[roles.r]\ngrants = [{ verbs = [\"read\"], on = [\"*\"] }]\n\n[[bind]]\nrole = \"r\"\nprincipals = [\"user:bob@auth.kelliher.info\"]\n"})
	require.NoError(t, err)
	require.Equal(t, uint64(3), ma.PolicyVersion())
	require.Equal(t, uint64(3), mb.PolicyVersion())
	require.False(t, nb.a.Policies.Allow(jkauthz.Subject{Principal: ana}, rpc.VerbSend, authz.Aria("aaaa0001")).Allowed, "the old version still granted")
	require.Equal(t, 3, policyEntries(t, na))
	require.Eventually(t, func() bool { return policyEntries(t, nb) == 3 }, 3*time.Second, 10*time.Millisecond)

	shown, err := callAs(t, na, owner(na), rpc.MethodTroupePolicy, rpc.TroupePolicyRequest{Troupe: "estate"})
	require.NoError(t, err)
	show := shown.(rpc.TroupePolicyResponse)
	require.Equal(t, uint64(3), show.InForce.Version)
	require.Equal(t, na.id(), show.InForce.Signer)
	require.Equal(t, authz.PrincipalText(rpc.Principal{Kind: rpc.PrincipalKind(owner(na).Kind), ID: owner(na).ID}), show.InForce.By)
	seen := map[string]uint64{}
	for _, row := range show.Nodes {
		seen[row.Node] = row.Policy
	}
	require.Equal(t, map[string]uint64{na.id(): 3, nb.id(): 3}, seen, "show did not carry what b holds after the exchange")
}

// The attack: c holds an intermediate of the same root but is no setter.
// Its owner cannot set a version through its door, and a version c's
// intermediate signs and pushes is adopted on no member.
func TestAnIntermediateThatIsNoSetterCannotPublishAPolicy(t *testing.T) {
	root := newEstateRoot(t)
	na, nc := newLedgerNode(t, root), newLedgerNode(t, root)
	na.policy, nc.policy = lendingEverything(), lendingEverything()
	ma, mc := na.start(), nc.start()
	_, err := ma.Merge(mc.Entries()...)
	require.NoError(t, err)
	rootStartsPolicy(t, na, root, anaHoldsEverything, na.id())
	_, err = mc.Merge(ma.Entries()...)
	require.NoError(t, err)
	require.Equal(t, uint64(1), mc.PolicyVersion())

	_, err = callAs(t, nc, owner(nc), rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: anaHoldsEverything + "\n# wider\n"})
	require.ErrorContains(t, err, "not this node")

	inter, ikey, err := nc.dir().Intermediate()
	require.NoError(t, err)
	forged, err := troupe.SignPolicy(ikey, inter, "estate", 7, "user:eve@auth.kelliher.info", "setters = [\""+nc.id()+"\"]\n[roles.r]\n", time.Now())
	require.NoError(t, err)
	for _, n := range []*ledgerNode{na, nc} {
		_, err = callAs(t, n, owner(n), rpc.MethodTroupeEntries, rpc.TroupeEntriesRequest{Troupe: "estate", Entries: troupe.Encode([]troupe.Entry{forged})})
		require.ErrorContains(t, err, "names that node a setter")
	}
	require.Equal(t, uint64(1), ma.PolicyVersion())
	require.Equal(t, uint64(1), mc.PolicyVersion())
	require.True(t, nc.a.Policies.Allow(jkauthz.Subject{Principal: ana}, rpc.VerbSend, authz.Aria("aaaa0001")).Allowed, "the root's version stopped granting")
}

// A document granting every verb on everything, in force on nodes that
// lend the troupe everything, still cannot change the troupe policy,
// revoke a node, or reach sandbox:local, on any node.
func TestAnAllGrantingTroupePolicyCannotTakeTheLocalOnlyRights(t *testing.T) {
	na, nb, _, mb, root := pairWith(t, lendingEverything())
	rootStartsPolicy(t, na, root, anaHoldsEverything, na.id(), nb.id())
	require.Equal(t, uint64(1), mb.PolicyVersion())

	revoke, err := troupe.SignRevoke(root.key, "estate", nb.id(), time.Now())
	require.NoError(t, err)
	for _, n := range []*ledgerNode{na, nb} {
		sub := jkauthz.Subject{Principal: ana}
		require.True(t, n.a.Policies.Allow(sub, rpc.VerbSend, authz.Aria("aaaa0001")).Allowed, "the document grants on %s", n.id())
		require.True(t, n.a.Policies.Allow(sub, rpc.VerbJoin, authz.Troupe("estate")).Allowed, "join stays the troupe's to grant")

		_, err := callAs(t, n, ana, rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: anaHoldsEverything + "\n# wider\n"})
		require.ErrorContains(t, err, "admin on troupe:estate", "ana changed the troupe policy on %s", n.id())

		_, err = callAs(t, n, ana, rpc.MethodTroupeEntries, rpc.TroupeEntriesRequest{Troupe: "estate", Entries: troupe.Encode([]troupe.Entry{revoke})})
		require.Error(t, err, "ana pushed a revoke on %s", n.id())

		_, err = callAs(t, n, ana, rpc.MethodTroupeLeave, rpc.TroupeLeaveRequest{Troupe: "estate"})
		require.Error(t, err)

		require.False(t, n.a.Policies.Allow(sub, rpc.VerbUse, authz.Sandbox("local")).Allowed, "ana reached sandbox:local on %s", n.id())
		require.False(t, n.a.Policies.Allow(sub, rpc.VerbAdmin, authz.Troupe("estate")).Allowed)
	}
	require.Empty(t, mb.View().Gone, "a revoke landed")
	require.Equal(t, uint64(1), mb.PolicyVersion())
}

func TestSettingTheTroupePolicyRefuses(t *testing.T) {
	na, nb, _, _, _ := pairWith(t, lendingEverything())
	for name, body := range map[string]string{
		"sandbox:local":               "[roles.s]\ngrants = [{ verbs = [\"use\"], on = [\"sandbox:local\"] }]\n",
		"admin on the troupe":         "[roles.a]\ngrants = [{ verbs = [\"admin\"], on = [\"troupe:estate\"] }]\n",
		"a body that is not a policy": "[roles.x]\ngrants = [{ verbs = [\"fly\"], on = [\"*\"] }]\n",
	} {
		_, err := callAs(t, na, owner(na), rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: body})
		require.Error(t, err, name)
	}
	_, err := callAs(t, nb, owner(nb), rpc.MethodTroupePolicySet, rpc.TroupePolicySetRequest{Troupe: "estate", Body: anaHoldsEverything})
	require.ErrorContains(t, err, "cannot sign", "a leaf-only node signed a policy")
	require.ErrorContains(t, err, na.id())
}
