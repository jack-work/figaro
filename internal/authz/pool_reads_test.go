package authz

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const poolDaemonPolicy = brokerGroupsPolicy + `
[roles.admin]
permissions = ["*:*"]

[[bind]]
role = "admin"
principals = ["user:gluck@auth.kelliher.info"]
`

var poolAdmin = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck@auth.kelliher.info"}

func guardedDaemon(t *testing.T, pool bool) map[string]jkrpc.HandlerFunc {
	t.Helper()
	p := NewPolicies(me, poolFacts())
	p.SetPoolDaemon(pool)
	p.SetIssuerScopes([]string{"auth.kelliher.info"})
	if err := p.Reload(writePolicy(t, poolDaemonPolicy)); err != nil {
		t.Fatal(err)
	}
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	handlers := map[string]jkrpc.HandlerFunc{}
	for _, m := range rpc.GuardedMethods() {
		handlers[m] = ok
	}
	return Guard(handlers, AriaHeader{}, Enforce(p))
}

func throughTheWebDoor(p jkauthz.Principal) context.Context {
	return WithDoor(WithSubject(context.Background(), subject(p)), DoorWeb)
}

type filteredRead struct{ method, form string }

func filteredReads() []filteredRead {
	reads := []filteredRead{{rpc.MethodList, ""}, {rpc.MethodNodeResolve, ""}, {rpc.MethodSet, rpc.TopologyFormID}}
	for _, form := range []string{rpc.AriasFormID, rpc.TopologyFormID, rpc.TroupeFormID, rpc.RecentFormID} {
		for _, m := range []string{rpc.MethodForm, rpc.MethodFollow, rpc.MethodUnfollow, rpc.MethodAttach} {
			reads = append(reads, filteredRead{m, form})
		}
	}
	return reads
}

// Ruling (c): on other daemons any role holder may list, resolve and
// follow, and sees only the rows it may read. broker holds a role on the
// pool daemon, so that rule would hand it an open stream (@recent) and
// an answer shaped by what exists. A daemon that leases workers decides
// those reads like any other call, which refuses broker and still
// serves the admin; hello is all broker keeps. figaro.list has no control
// row: every daemon refuses it to a principal that reads no figaro (D7).
func TestAPoolDaemonRefusesBrokerTheReadsOtherDaemonsFilter(t *testing.T) {
	pool, plain := guardedDaemon(t, true), guardedDaemon(t, false)
	broker, admin := throughTheWebDoor(brokerClient), throughTheWebDoor(poolAdmin)
	for _, c := range filteredReads() {
		if err := call(t, pool, broker, c.method, c.form); err == nil {
			t.Errorf("a pool daemon served broker %s %s", c.method, c.form)
		}
		if err := call(t, pool, admin, c.method, c.form); err != nil {
			t.Errorf("a pool daemon refused its admin %s %s: %v", c.method, c.form, err)
		}
		if c.method == rpc.MethodList {
			continue
		}
		if err := call(t, plain, broker, c.method, c.form); err != nil {
			t.Errorf("%s %s was refused to a role holder on a daemon that leases nothing, so the rows above do not show the pool refusing it: %v", c.method, c.form, err)
		}
	}
	if err := call(t, pool, broker, rpc.MethodHello, ""); err != nil {
		t.Errorf("a pool daemon refused broker %s: %v", rpc.MethodHello, err)
	}
}

// The broker row of the test plan: holding lease on one pool, broker is
// served hello and the lease methods for that pool. Every other guarded
// method is refused, whatever it names, and the lease methods are
// refused for another pool.
func TestOnAPoolDaemonBrokerIsServedHelloAndItsOwnPoolsLeaseMethods(t *testing.T) {
	h := guardedDaemon(t, true)
	broker := throughTheWebDoor(brokerClient)
	var served []string
	for _, m := range rpc.GuardedMethods() {
		verb, _ := rpc.VerbOf(m)
		if verb == rpc.VerbLease {
			if _, err := h[m](broker, json.RawMessage(`{"pool":"chat-groups"}`)); err != nil {
				t.Errorf("%s on broker's own pool: %v", m, err)
			}
			for _, params := range []string{`{"pool":"chat-group"}`, `{"pool":"others"}`, `{"pool":""}`, `{}`, `{"figaro_id":"chat-groups"}`} {
				if _, err := h[m](broker, json.RawMessage(params)); err == nil {
					t.Errorf("%s %s was served to broker", m, params)
				}
			}
			continue
		}
		for _, aria := range []string{"", "in-a", "owners", "never-minted", rpc.AriasFormID, rpc.TopologyFormID, rpc.TroupeFormID, rpc.RecentFormID} {
			if err := call(t, h, broker, m, aria); err == nil {
				served = append(served, m)
				break
			}
		}
		if _, err := h[m](broker, json.RawMessage(`{"pool":"chat-groups"}`)); err == nil && !slices.Contains(served, m) {
			served = append(served, m)
		}
	}
	if want := []string{rpc.MethodHello}; !slices.Equal(served, want) {
		t.Errorf("beside the lease methods a pool daemon served broker %v, want %v", served, want)
	}
}

var leftToTheirHandlersElsewhere = []string{
	rpc.MethodGCApply,
	rpc.MethodGCPlan,
	rpc.MethodGCUndo,
	rpc.MethodImportStatus,
	rpc.MethodNodeLearn,
	rpc.MethodTroupeAPIKeyList,
	rpc.MethodTroupeAPIKeyMint,
	rpc.MethodTroupeAPIKeyRevoke,
	rpc.MethodTroupeEntries,
	rpc.MethodTroupeIdentity,
	rpc.MethodTroupeLeave,
	rpc.MethodTroupePolicySet,
	rpc.MethodTroupeSign,
}

// Other daemons pass these methods at the guard and let the handler
// decide, and one of them (troupe.apikey.ls) answers any caller. A pool
// daemon is in no troupe and has no node door, so its guard decides
// each by its verb on the daemon: broker is refused and the admin
// passes. A method newly left to its handler has no row here and fails
// the test until someone shows what a pool daemon does with it.
func TestAPoolDaemonsGuardDecidesEachMethodOtherDaemonsLeaveToItsHandler(t *testing.T) {
	var left []string
	for _, m := range rpc.GuardedMethods() {
		if checkedByHandler(m) {
			left = append(left, m)
		}
	}
	rows := slices.Sorted(slices.Values(leftToTheirHandlersElsewhere))
	if !slices.Equal(left, rows) {
		t.Fatalf("the guard leaves %v to their handlers and this table has rows for %v", left, rows)
	}
	pool, plain := guardedDaemon(t, true), guardedDaemon(t, false)
	broker, admin := throughTheWebDoor(brokerClient), throughTheWebDoor(poolAdmin)
	for _, m := range rows {
		for _, aria := range []string{"", "in-a", rpc.AriasFormID} {
			if err := call(t, pool, broker, m, aria); err == nil {
				t.Errorf("a pool daemon's guard passed broker's %s on %q to its handler", m, aria)
			}
		}
		if _, err := pool[m](broker, json.RawMessage(`{"pool":"chat-groups"}`)); err == nil {
			t.Errorf("a pool daemon's guard passed broker's %s to its handler when it named broker's pool", m)
		}
		if err := call(t, pool, admin, m, ""); err != nil {
			t.Errorf("a pool daemon's guard refused its admin %s: %v", m, err)
		}
		if err := call(t, plain, broker, m, ""); err != nil {
			t.Errorf("%s did not reach its handler on a daemon that leases nothing, so the pool is not what refuses broker above: %v", m, err)
		}
	}
}
