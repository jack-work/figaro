package authz

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func narrowedButUnreadable(t *testing.T, own map[string]string, broken ...string) *Policies {
	t.Helper()
	f := lineageFakes(own)
	f.owners["kid"] = me
	f.broken = map[string]bool{}
	for _, id := range broken {
		f.broken[id] = true
	}
	p := NewPolicies(me, f)
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestASuccessionIsRefusedWhenAScopeInTheChainCannotBeRead(t *testing.T) {
	own := map[string]string{"top": `["read:*"]`}
	for _, broken := range []string{"top", "kid"} {
		p := narrowedButUnreadable(t, own, broken)
		scopes, narrowed, held, err := p.SuccessorScopes("kid")
		if err == nil {
			t.Errorf("%s unreadable: scopes=%q narrowed=%v held=%v, want the succession refused", broken, scopes, narrowed, held)
		}
	}
}

func TestAFigaroWhoseScopesCannotBeReadIsRefusedAndToldSo(t *testing.T) {
	p := narrowedButUnreadable(t, map[string]string{"kid": `["read:*"]`}, "top")
	d := p.AllowFigaro(subject(me), "kid", rpc.VerbRead, Aria("aaaa0001"))
	if d.Allowed || !strings.Contains(d.Reason(), "cannot be read") {
		t.Fatalf("allowed=%v: %s", d.Allowed, d.Reason())
	}
	if err := p.HoldsGrants("kid", nil, "kid"); err == nil {
		t.Fatal("a figaro whose scopes cannot be read was found to hold what it gives")
	}
}
