package authz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const listerOnly = `
[roles.lister]
permissions = ["list:*"]

[roles.scoped]
permissions = ["list:lineage:aaaa0001"]

[[bind]]
role = "lister"
principals = ["user:lister"]

[[bind]]
role = "scoped"
principals = ["user:scoped"]
`

func TestAListingGrantPassesTheListMethodAndNothingElse(t *testing.T) {
	p := loaded(t, listerOnly)
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodList: ok, rpc.MethodRead: ok, rpc.MethodForm: ok, rpc.MethodFollow: ok, rpc.MethodQua: ok,
	}, AriaHeader{}, Enforce(p))
	for _, who := range []string{"lister", "scoped"} {
		ctx := WithSubject(context.Background(), subject(jkauthz.Principal{Kind: jkauthz.KindUser, ID: who}))
		if err := call(t, h, ctx, rpc.MethodList, ""); err != nil {
			t.Errorf("%s: figaro.list: %v", who, err)
		}
		for _, m := range []string{rpc.MethodRead, rpc.MethodForm, rpc.MethodFollow, rpc.MethodQua} {
			if err := call(t, h, ctx, m, "aaaa0001"); err == nil {
				t.Errorf("%s: %s on an aria it may only list was served", who, m)
			}
		}
	}
	nobody := WithSubject(context.Background(), subject(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "nobody"}))
	if err := call(t, h, nobody, rpc.MethodList, ""); err == nil {
		t.Error("a principal with no role listed")
	}
}

func TestAListingGrantIsWholeRowsAndTakesNoKeyGlob(t *testing.T) {
	if _, err := ParseFigaroGrants([]string{"list:*", "list:lineage:aaaa0001", "list:aria:self"}); err != nil {
		t.Fatalf("plain list grants: %v", err)
	}
	if _, err := ParseFigaroGrants([]string{"list:aria:aaaa0001#mantra"}); err == nil || !strings.Contains(err.Error(), "list") {
		t.Fatalf("a keyed list grant parsed: %v", err)
	}
}

func TestReadDoesNotCoverListInTheGrantAlgebraButStarDoes(t *testing.T) {
	m := selectors{}
	if m.grantCovers(jkauthz.Grant{Verb: "read", Object: "*"}, jkauthz.Grant{Verb: "list", Object: "*"}) {
		t.Error("read:* covers list:* in the algebra; the listing check reads both verbs instead")
	}
	if !m.grantCovers(jkauthz.Grant{Verb: "*", Object: "*"}, jkauthz.Grant{Verb: "list", Object: "*"}) {
		t.Error("*:* does not cover list:*")
	}
}
