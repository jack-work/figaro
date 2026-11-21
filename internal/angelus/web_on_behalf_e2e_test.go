package angelus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/sdk"
)

const onBehalfPolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.guest]
permissions = ["read:owner:self", "listen:owner:self", "send:owner:self"]

[[bind]]
role = "owner"
principals = ["user:UID", "user:gluck@issuer.test.figaro"]

[[bind]]
role = "guest"
principals = ["user:ana@issuer.test.figaro"]

[roles.gateway]
permissions = ["read:*", "listen:*", "send:*", "fork:*", "set:*", "mint:*", "use:sandbox:local"]

[[bind]]
role = "gateway"
principals = ["service:figchat-gateway@issuer.test.figaro"]
`

type onBehalfRig struct {
	path   string
	dir    string
	aria   string
	mint   func(client string) string
	daemon *angelus.Angelus
	owner  *sdk.Angelus
	ctx    context.Context
}

const plainMockOutfit = "[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"

func onBehalfDoor(t *testing.T) onBehalfRig {
	t.Helper()
	return onBehalfDoorWith(t, plainMockOutfit, "")
}

func onBehalfDoorWith(t *testing.T, mockOutfit, authzExtra string) onBehalfRig {
	t.Helper()
	is := peertest.NewIssuer(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte(mockOutfit), 0o600))
	require.NoError(t, os.WriteFile(dir+"/outfits/unboxed.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\nsandbox = \"none\"\ntools = [\"bash\"]\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/outfits/scoped.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\nscopes = [\"read:*\"]\n"), 0o600))
	policy := filepath.Join(dir, "policy.toml")
	require.NoError(t, os.WriteFile(policy, []byte(strings.ReplaceAll(onBehalfPolicy, "UID", strconv.Itoa(os.Getuid()))), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte(`default_outfit = "mock"
[authz]
policy = "`+policy+`"
`+authzExtra+`

[[authz.issuers]]
issuer = "`+peertest.IssuerURL+`"
jwks = "`+is.JWKS.URL+`"
clients = { figchat-gateway = "service", other-service = "service" }

[[authz.delegation]]
principal = "service:figchat-gateway@issuer.test.figaro"
users = ["user:gluck@issuer.test.figaro"]
`), 0o600))
	a, acli, ctx := daemonOnStore(t, dir, nil)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	loaded, err := config.Load(dir)
	require.NoError(t, err)
	issuers, err := authz.NewIssuers(loaded.Config.Authz.Issuers, nil, nil)
	require.NoError(t, err)
	delegations, err := authz.NewDelegations(loaded.Config.Authz.Delegation)
	require.NoError(t, err)
	a.Issuers, a.Delegations = issuers, delegations

	path := filepath.Join(filepath.Dir(a.SocketPath), "web.sock")
	ln, err := angelus.ListenUnix(path, 0o660)
	require.NoError(t, err)
	go a.ServeWeb(ctx, ln, angelus.Web{Admit: authz.Bearers{Issuers: issuers, Delegations: delegations}.AdmitRequest})
	return onBehalfRig{path: path, dir: dir, aria: created.FigaroID, daemon: a, owner: acli, ctx: ctx, mint: func(client string) string { return is.Mint(client, time.Hour) }}
}

func (r onBehalfRig) as(t *testing.T, client, user string) (*edgeClient, *http.Response, error) {
	t.Helper()
	h := http.Header{"Authorization": {"Bearer " + r.mint(client)}}
	if user != "" {
		h.Set(authz.DelegateHeader, user)
	}
	return forge(t, r.path, h)
}

func TestAServiceActsAsTheUserItNamesWithinTheDelegableVerbs(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	var hello rpc.HelloResponse
	reply := c.call(rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion})
	require.Nil(t, reply.Error)
	require.NoError(t, json.Unmarshal(reply.Result, &hello))
	require.Equal(t, rpc.Principal{Kind: rpc.PrincipalUser, ID: "gluck@issuer.test.figaro"}, hello.Principal)

	for _, m := range []string{rpc.MethodContext, rpc.MethodForm, rpc.MethodList} {
		reply = c.call(m, onAria(r.aria, nil))
		require.Nil(t, reply.Error, m)
	}

	reply = c.call(rpc.MethodQua, onAria(r.aria, map[string]any{"text": "spoken through the gateway", rpc.CallerKey: "deadbeef", "x-caller": map[string]any{"label": "root"}}))
	require.Nil(t, reply.Error)
	waitIdle(t, r.daemon, r.aria)

	reply = c.call(rpc.MethodRead, onAria(r.aria, nil))
	require.Nil(t, reply.Error)
	var page aria.Page
	require.NoError(t, json.Unmarshal(reply.Result, &page))
	var senders []string
	for _, part := range page.Parts {
		for _, s := range part.InquirySegments {
			senders = append(senders, s.Sender)
		}
	}
	require.Equal(t, []string{"gluck"}, senders, "the message lands with the user as its sender, whatever the call asserted")

	reply = c.call(rpc.MethodFork, onAria(r.aria, nil))
	require.Nil(t, reply.Error)
	reply = c.call(rpc.MethodInterrupt, onAria(r.aria, nil))
	require.Nil(t, reply.Error)
}

func TestADelegateIsRefusedWhatTheUserMayDo(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	for name, call := range map[string]struct {
		method string
		params map[string]any
	}{
		"kill":                          {rpc.MethodKill, onAria(r.aria, nil)},
		"a set that writes system.ttl":  {rpc.MethodSet, onAria(r.aria, map[string]any{"patch": ttlPatch()})},
		"a fork that writes system.ttl": {rpc.MethodFork, onAria(r.aria, map[string]any{"patch": ttlPatch()})},
		"reroute":                       {rpc.MethodReroute, onAria(r.aria, nil)},
		"a system status call":          {rpc.MethodStatus, nil},
		"gc":                            {rpc.MethodGC, nil},
		"form.create":                   {rpc.MethodFormCreate, map[string]any{"patch": plainPatch(t, map[string]string{"mantra": "x"})}},
		"form.bind":                     {rpc.MethodFormBind, nil},
		"import":                        {rpc.MethodImport, nil},
		"a send that sets the model": {rpc.MethodQua, onAria(r.aria, map[string]any{
			"text": "x", "form": map[string]any{"context": map[string]any{"mantra": "x"}, "outfits": []string{"mock"}}})},
	} {
		reply := c.call(call.method, call.params)
		require.NotNil(t, reply.Error, name)
		require.Contains(t, reply.Error.Message, "acts for user:gluck@issuer.test.figaro", name)
	}
	reply := c.call(rpc.MethodContext, onAria(r.aria, nil))
	require.Nil(t, reply.Error, "a refusal leaves the connection and its reads alone")
}

func TestARefusedDelegationIsAClearErrorNotTheServicePrincipal(t *testing.T) {
	r := onBehalfDoor(t)
	for name, c := range map[string]struct {
		client, user, want string
	}{
		"a user the owner did not list":    {"figchat-gateway", "ana", "not listed to act for user:ana@issuer.test.figaro"},
		"a user nobody has heard of":       {"figchat-gateway", "mallory", "not listed to act for"},
		"a service the owner did not list": {"other-service", "gluck", "service:other-service@issuer.test.figaro is not listed"},
		"a name that is a principal":       {"figchat-gateway", "user:gluck@issuer.test.figaro", "not a user name"},
		"an empty name":                    {"figchat-gateway", " ", "names no user"},
	} {
		h := http.Header{"Authorization": {"Bearer " + r.mint(c.client)}}
		h.Set(authz.DelegateHeader, c.user)
		_, resp, err := forge(t, r.path, h)
		require.Error(t, err, name)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		require.Contains(t, string(body[:n]), c.want, name)
	}

	h := http.Header{"Authorization": {"Bearer " + r.mint("figchat-gateway")}}
	h.Add(authz.DelegateHeader, "gluck")
	h.Add(authz.DelegateHeader, "ana")
	_, resp, err := forge(t, r.path, h)
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func plainPatch(t *testing.T, kv map[string]string) form.Patch {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		set[k] = b
	}
	return form.FromMap(set).AsPatch()
}

func ttlPatch() form.Patch {
	return form.FromMap(map[string]json.RawMessage{"system.ttl": json.RawMessage(`"1h"`)}).AsPatch()
}

func storeHolds(t *testing.T, dir string, needles ...string) bool {
	t.Helper()
	found := false
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		all := true
		for _, n := range needles {
			all = all && bytes.Contains(raw, []byte(n))
		}
		found = found || all
		return nil
	}))
	return found
}

func TestADelegatedSetLandsInTheFormLogAsTheUser(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	reply := c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"mantra": "set through the gateway"}), rpc.CallerKey: "deadbeef", "x-caller": map[string]any{"label": "root"}}))
	require.Nil(t, reply.Error)
	var set rpc.SetResponse
	require.NoError(t, json.Unmarshal(reply.Result, &set))
	require.Equal(t, rpc.OutcomeApplied, set.Outcome)

	reply = c.call(rpc.MethodForm, onAria(r.aria, nil))
	require.Nil(t, reply.Error)
	var got rpc.FormResponse
	require.NoError(t, json.Unmarshal(reply.Result, &got))
	require.Equal(t, "set through the gateway", *got.Snapshot.Lookup("mantra"))

	require.True(t, storeHolds(t, r.dir, "set through the gateway", `"principal":"user:gluck@issuer.test.figaro"`), "the form log names the user as the writer")
	require.False(t, storeHolds(t, r.dir, "set through the gateway", "figchat-gateway"), "the form log names the user, not the service")

	reply = c.call(rpc.MethodSet, onAria(r.aria, map[string]any{"patch": form.Patch{Object: &form.ObjectPatch{Delete: map[string]form.Value{"mantra": form.NewValue(json.RawMessage(`"set through the gateway"`))}}}}))
	require.Nil(t, reply.Error)
	reply = c.call(rpc.MethodForm, onAria(r.aria, nil))
	require.NoError(t, json.Unmarshal(reply.Result, &got))
	require.Nil(t, got.Snapshot.Lookup("mantra"), "an unset lands too")
}

func TestADelegatedForkCarriesATitleAndRefusesSystemKeys(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	reply := c.call(rpc.MethodFork, onAria(r.aria, map[string]any{"patch": plainPatch(t, map[string]string{"title": "a side road"})}))
	require.Nil(t, reply.Error)
	reply = c.call(rpc.MethodFork, onAria(r.aria, map[string]any{"patch": ttlPatch()}))
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "system.ttl is daemon/owner-held")
}
