package angelus_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/workerpool"
	"github.com/jack-work/jkrpc"
)

func brokerOnAPoolWithALiveLease(t *testing.T) (r grantRig, secrets map[string]string) {
	t.Helper()
	r = pagesRig(t)
	r.a.Policies.SetPoolDaemon(true)
	box, err := r.a.Settings.LeaseBox("groups", "worker0", 1)
	if err != nil {
		t.Fatal(err)
	}
	outfit := "[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \"" + box + "\"\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.policy), "outfits", "boxed.toml"), []byte(outfit), 0o600); err != nil {
		t.Fatal(err)
	}
	leaseTo(t, r.a.Workers.Ledger, "worker0", "chat:10", 1, workerpool.Live)
	leased, err := r.create(asOwner(), "boxed")
	if err != nil {
		t.Fatal(err)
	}
	return r, map[string]string{"the leased figaro": leased.FigaroID, "the box": box, "the worker": "worker0", "the lease key": "chat:10", "the say bearer": "bearer-of-worker0", "a form id": `"@`}
}

// Ruling (c): hello is the one call broker keeps beside its lease
// methods, so its answer must say nothing about what the pool holds:
// the protocol, the node and broker's own name, and no figaro, form,
// worker, lease or bearer.
func TestHelloTellsBrokerNothingAboutThePool(t *testing.T) {
	r, secrets := brokerOnAPoolWithALiveLease(t)
	out, err := r.call(asBroker(), rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion})
	if err != nil {
		t.Fatalf("hello as broker: %v", err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"capabilities", "node", "principal", "version"}; !slices.Equal(names, want) {
		t.Errorf("hello answers the fields %v, want %v: a new one needs showing that it names nothing the pool holds", names, want)
	}
	if got, want := string(fields["principal"]), `{"kind":"user","id":"broker"}`; got != want {
		t.Errorf("hello names the principal %s, want the caller's own, %s", got, want)
	}
	for what, secret := range secrets {
		if strings.Contains(string(body), secret) {
			t.Errorf("hello's answer to broker carries %s (%q): %s", what, secret, body)
		}
	}
}

// The same ruling through the daemon's own guard and handlers, with a
// figaro to hide. Every answer broker gets is the guard's refusal or the
// not-found that hides a form: no handler runs far enough to complain
// about its params instead, which is what troupe.apikey.ls and
// figaro.gc.undo did while the guard left them to their handlers.
func TestAPoolDaemonsHandlersRefuseBrokerEverythingButHelloAndItsPool(t *testing.T) {
	r, secrets := brokerOnAPoolWithALiveLease(t)
	leased := secrets["the leased figaro"]
	for _, m := range rpc.GuardedMethods() {
		verb, _ := rpc.VerbOf(m)
		if verb == rpc.VerbNone || verb == rpc.VerbLease || r.h[m] == nil {
			continue
		}
		for _, id := range []string{"", leased, rpc.AriasFormID, rpc.TopologyFormID, rpc.RecentFormID} {
			out, err := r.h[m](asBroker(), withAria(t, id, struct{}{}))
			if err == nil {
				t.Errorf("%s on %q was served to broker: %v", m, id, out)
				continue
			}
			var refusal *jkrpc.Error
			if (!errors.As(err, &refusal) || refusal.Code != authz.ErrCode) && err.Error() != authz.NotFound(m, id).Error() {
				t.Errorf("%s on %q reached its handler as broker: %v", m, id, err)
			}
		}
	}
	if _, err := r.h[rpc.MethodList](asOwner(), json.RawMessage(`{}`)); err != nil {
		t.Errorf("the pool daemon's owner was refused %s: %v", rpc.MethodList, err)
	}
}
