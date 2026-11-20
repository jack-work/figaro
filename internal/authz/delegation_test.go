package authz

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const (
	gatewayText = "service:figchat-gateway@auth.kelliher.info"
	gluckText   = "user:gluck@auth.kelliher.info"
)

var (
	gatewayP = jkauthz.Principal{Kind: KindService, ID: "figchat-gateway@auth.kelliher.info"}
	gluckP   = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck@auth.kelliher.info"}
)

func delegationsFor(t *testing.T, users ...string) *Delegations {
	t.Helper()
	d, err := NewDelegations([]config.DelegationConfig{{Principal: gatewayText, Users: users}})
	require.NoError(t, err)
	return d
}

func gatewaySession(t *testing.T) context.Context {
	t.Helper()
	is := newTestIssuer(t)
	cfg := is.config()
	cfg.Clients = map[string]string{"figchat-gateway": "service", "other-svc": "service"}
	issuers, err := NewIssuers([]config.IssuerConfig{cfg}, nil, is.clock)
	require.NoError(t, err)
	s, err := issuers.Open(context.Background(), is.mint("figchat-gateway", "figchat-gateway", time.Hour, nil))
	require.NoError(t, err)
	return WithSession(context.Background(), s)
}

func TestADelegationNamesAServiceAndUsersOfItsIssuer(t *testing.T) {
	for name, cfg := range map[string]config.DelegationConfig{
		"a user as the delegate":   {Principal: gluckText, Users: []string{gluckText}},
		"no issuer scope":          {Principal: "service:figchat-gateway", Users: []string{gluckText}},
		"no users":                 {Principal: gatewayText},
		"a service as the user":    {Principal: gatewayText, Users: []string{gatewayText}},
		"a user of another issuer": {Principal: gatewayText, Users: []string{"user:gluck@id.example.org"}},
		"a bare user":              {Principal: gatewayText, Users: []string{"user:gluck"}},
		"a node-qualified service": {Principal: "spain/" + gatewayText, Users: []string{gluckText}},
	} {
		_, err := NewDelegations([]config.DelegationConfig{cfg})
		require.Error(t, err, name)
	}
	_, err := NewDelegations([]config.DelegationConfig{{Principal: gatewayText, Users: []string{gluckText}}, {Principal: gatewayText, Users: []string{gluckText}}})
	require.ErrorContains(t, err, "twice")
}

func TestAListedServiceActsAsTheUserItNames(t *testing.T) {
	ctx, err := delegationsFor(t, gluckText).Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)

	sub, err := SubjectOf(ctx)
	require.NoError(t, err)
	require.Equal(t, gluckP, sub.Principal)
	via, ok := DelegatedVia(sub)
	require.True(t, ok)
	require.Equal(t, gatewayP, via)
	require.Equal(t, rpc.Principal{Kind: rpc.PrincipalUser, ID: gluckP.ID}, PrincipalOf(ctx))
	name, ok := OnBehalfOf(ctx)
	require.True(t, ok)
	require.Equal(t, "gluck", name)
	token, ok := BearerOf(ctx)
	require.True(t, ok, "the service's own token is what a forwarded call carries")
	require.NotEmpty(t, token)
}

func TestADelegationIsRefusedOutright(t *testing.T) {
	ctx := gatewaySession(t)
	for name, c := range map[string]struct {
		d    *Delegations
		user string
		want string
	}{
		"an unlisted user":     {delegationsFor(t, gluckText), "ana", "not listed to act for user:ana@auth.kelliher.info"},
		"an unlisted service":  {mustDelegations(t, config.DelegationConfig{Principal: "service:other-svc@auth.kelliher.info", Users: []string{gluckText}}), "gluck", "figchat-gateway@auth.kelliher.info is not listed"},
		"no delegation at all": {nil, "gluck", "lists no service"},
		"no name":              {delegationsFor(t, gluckText), "", "names no user"},
		"a name with a scope":  {delegationsFor(t, gluckText), "gluck@id.example.org", "not a user name"},
		"a name with a kind":   {delegationsFor(t, gluckText), "user:gluck", "not a user name"},
		"a name with a glob":   {delegationsFor(t, gluckText), "*", "not a user name"},
	} {
		got, err := c.d.Apply(ctx, c.user)
		require.Nil(t, got, name)
		var de *DelegationError
		require.ErrorAs(t, err, &de, name)
		require.ErrorContains(t, err, c.want, name)
	}
}

func mustDelegations(t *testing.T, cfgs ...config.DelegationConfig) *Delegations {
	t.Helper()
	d, err := NewDelegations(cfgs)
	require.NoError(t, err)
	return d
}

func TestOnlyABearerCallerMayDelegate(t *testing.T) {
	d := delegationsFor(t, gluckText)
	for name, ctx := range map[string]context.Context{
		"a peer by uid":   WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalKind("service"), ID: "figchat-gateway@auth.kelliher.info"}),
		"an edge subject": WithSubject(context.Background(), jkauthz.Subject{Principal: gatewayP}),
	} {
		_, err := d.Apply(ctx, "gluck")
		require.ErrorContains(t, err, "admitted by a bearer", name)
	}
}

func TestADelegatedConnectionDoesNotDelegateAgain(t *testing.T) {
	d := delegationsFor(t, gluckText)
	ctx, err := d.Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)
	_, err = d.Apply(ctx, "gluck")
	require.ErrorContains(t, err, "already acts for")
}

func delegatePolicy(t *testing.T) *Policies {
	t.Helper()
	return loaded(t, `
[roles.owner]
permissions = ["*:*"]

[[bind]]
role = "owner"
principals = ["`+gluckText+`"]

[roles.gateway]
permissions = ["read:*", "listen:*", "send:*", "fork:*", "use:sandbox:local"]

[[bind]]
role = "gateway"
principals = ["`+gatewayText+`"]
`)
}

func TestADelegateIsNoWiderThanTheServiceItself(t *testing.T) {
	p := loaded(t, `
[roles.owner]
permissions = ["*:*"]

[roles.narrow]
permissions = ["read:aria:aaaa0002"]

[[bind]]
role = "owner"
principals = ["`+gluckText+`"]

[[bind]]
role = "narrow"
principals = ["`+gatewayText+`"]
`)
	ctx, err := delegationsFor(t, gluckText).Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)
	sub, err := SubjectOf(ctx)
	require.NoError(t, err)
	require.True(t, p.Allow(sub, rpc.VerbRead, Aria("aaaa0002")).Allowed)
	for _, verb := range []rpc.Verb{rpc.VerbSend, rpc.VerbFork, rpc.VerbListen} {
		d := p.Allow(sub, verb, Aria("aaaa0002"))
		require.False(t, d.Allowed, verb)
		require.Contains(t, d.Reason(), "itself may not")
	}
	require.False(t, p.Allow(sub, rpc.VerbRead, Aria("aaaa0001")).Allowed)
	none := loaded(t, "[roles.owner]\npermissions = [\"*:*\"]\n[[bind]]\nrole = \"owner\"\nprincipals = [\""+gluckText+"\"]\n")
	require.False(t, none.Allow(sub, rpc.VerbSend, Aria("aaaa0002")).Allowed, "a service the policy does not name holds nothing")
}

func TestARemovedDelegationEndsALiveDelegatedConnection(t *testing.T) {
	d := delegationsFor(t, gluckText)
	ctx, err := d.Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)
	_, err = SubjectOf(ctx)
	require.NoError(t, err)
	require.NoError(t, d.Reload(nil))
	_, err = SubjectOf(ctx)
	require.ErrorContains(t, err, "no longer listed")
}

func TestOnlyADelegatedCallerHasASenderLabel(t *testing.T) {
	plain := gatewaySession(t)
	raw := json.RawMessage(`{"x-caller":{"label":"gluck"}}`)
	require.Equal(t, "", rpc.SenderFrom(pinnedToDelegate(plain, raw), nil), "a service typing the name is not the delegated user")
	duke := pinnedToDelegate(plain, json.RawMessage(`{"x-caller":{"duke":true,"label":"x"}}`))
	require.Equal(t, rpc.DefaultDukeTitle, rpc.SenderFrom(duke, nil))
	ctx, err := delegationsFor(t, gluckText).Apply(plain, "gluck")
	require.NoError(t, err)
	require.Equal(t, "gluck", rpc.SenderFrom(pinnedToDelegate(ctx, raw), nil))
}

func TestADelegateHoldsTheUsersGrantsOnlyForTheCeilingVerbs(t *testing.T) {
	p := delegatePolicy(t)
	user := jkauthz.Subject{Principal: gluckP}
	via, _ := (delegationsFor(t, gluckText)).Apply(gatewaySession(t), "gluck")
	sub, err := SubjectOf(via)
	require.NoError(t, err)

	for _, verb := range []rpc.Verb{rpc.VerbRead, rpc.VerbListen, rpc.VerbSend, rpc.VerbFork} {
		require.True(t, p.Allow(user, verb, Aria("aaaa0002")).Allowed, "the user holds %s", verb)
		require.True(t, p.Allow(sub, verb, Aria("aaaa0002")).Allowed, "the delegate passes %s", verb)
	}
	require.True(t, p.Allow(sub, rpc.VerbUse, Sandbox("local")).Allowed)
	require.True(t, p.Allow(sub, rpc.VerbUse, Sandbox("")).Allowed)
	for _, verb := range []rpc.Verb{rpc.VerbKill, rpc.VerbSet, rpc.VerbAdmin, rpc.VerbCreate, rpc.VerbMint, rpc.VerbDelete, rpc.VerbCast, rpc.VerbSucceed, rpc.VerbAdopt, rpc.VerbDecide} {
		require.True(t, p.Allow(user, verb, Aria("aaaa0002")).Allowed, "the user holds %s", verb)
		d := p.Allow(sub, verb, Aria("aaaa0002"))
		require.False(t, d.Allowed, "the delegate is refused %s", verb)
		require.Contains(t, d.Reason(), "acts for "+gluckText)
	}
	require.False(t, p.Allow(sub, rpc.VerbUse, Sandbox("figbox")).Allowed)
	require.False(t, p.Allow(sub, rpc.VerbSet, Daemon).Allowed)
}

func TestTheGuardRefusesWhatADelegateMayNotCallAndPinsItsSender(t *testing.T) {
	p := delegatePolicy(t)
	var seen json.RawMessage
	call := func(_ context.Context, params json.RawMessage) (any, error) {
		seen = params
		return "ok", nil
	}
	guarded := Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodRead: call, rpc.MethodQua: call, rpc.MethodFork: call, rpc.MethodKill: call, rpc.MethodSet: call,
		rpc.MethodStatus: call, rpc.MethodFormApply: call, rpc.MethodHello: call,
	}, AriaHeader{}, Enforce(p))
	ctx, err := delegationsFor(t, gluckText).Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)
	ctx = WithDoor(ctx, DoorWeb)

	aria := `"` + rpc.AriaKey + `":"aaaa0002"`
	for _, m := range []string{rpc.MethodRead, rpc.MethodHello} {
		_, err := guarded[m](ctx, json.RawMessage(`{`+aria+`}`))
		require.NoError(t, err, m)
	}
	for _, m := range []string{rpc.MethodKill, rpc.MethodSet, rpc.MethodStatus, rpc.MethodFormApply} {
		_, err := guarded[m](ctx, json.RawMessage(`{`+aria+`}`))
		var je *jkrpc.Error
		require.True(t, errors.As(err, &je), m)
		require.Equal(t, ErrCode, je.Code, m)
		require.Contains(t, je.Message, "acts for "+gluckText, m)
	}

	_, err = guarded[rpc.MethodQua](ctx, json.RawMessage(`{`+aria+`,"text":"hi","`+rpc.CallerKey+`":"bbbb0001","x-caller":{"label":"root"}}`))
	require.NoError(t, err)
	require.Empty(t, rpc.CallerOf(seen), "a delegate claims no figaro")
	require.Equal(t, "gluck", rpc.SenderFrom(seen, nil), "the sender is the user it acts for, whatever the params assert")
}

func TestADelegatedSendAndForkCarryNoWrites(t *testing.T) {
	p := delegatePolicy(t)
	call := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	guarded := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: call, rpc.MethodFork: call}, AriaHeader{}, Enforce(p))
	ctx, err := delegationsFor(t, gluckText).Apply(gatewaySession(t), "gluck")
	require.NoError(t, err)
	ctx = WithDoor(ctx, DoorWeb)

	model := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"x"`)}, nil)
	body := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		fields := map[string]json.RawMessage{}
		require.NoError(t, json.Unmarshal(raw, &fields))
		fields[rpc.AriaKey] = json.RawMessage(`"aaaa0002"`)
		raw, err = json.Marshal(fields)
		require.NoError(t, err)
		return raw
	}
	refused := map[string]struct {
		method string
		params any
	}{
		"a system patch on a send": {rpc.MethodQua, rpc.QuaRequest{Text: "x", Form: &rpc.FormInput{Patch: &model}}},
		"outfits on a send":        {rpc.MethodQua, rpc.QuaRequest{Text: "x", Form: &rpc.FormInput{Outfits: []string{"root"}}}},
		"a context write on a send": {rpc.MethodQua, rpc.QuaRequest{Text: "x", Form: &rpc.FormInput{
			Context: map[string]json.RawMessage{"mantra": json.RawMessage(`"x"`)}}}},
		"a patch on a fork":    {rpc.MethodFork, rpc.ForkRequest{Patch: &model}},
		"outfits on a fork":    {rpc.MethodFork, rpc.ForkRequest{Outfits: []string{"root"}}},
		"a seat on a fork":     {rpc.MethodFork, rpc.ForkRequest{Seat: "@role"}},
		"abdication on a fork": {rpc.MethodFork, rpc.ForkRequest{Abdicates: true}},
	}
	for name, c := range refused {
		_, err := guarded[c.method](ctx, body(c.params))
		require.ErrorContains(t, err, "acts for "+gluckText, name)
	}
	allowed := map[string]struct {
		method string
		params any
	}{
		"a plain send": {rpc.MethodQua, rpc.QuaRequest{Text: "x"}},
		"a send carrying the client's view of system state": {rpc.MethodQua, rpc.QuaRequest{Text: "x", Form: &rpc.FormInput{
			Context: map[string]json.RawMessage{"system.model": json.RawMessage(`"x"`)}}}},
		"a plain fork": {rpc.MethodFork, rpc.ForkRequest{AtTurn: 2}},
	}
	for name, c := range allowed {
		_, err := guarded[c.method](ctx, body(c.params))
		require.NoError(t, err, name)
	}
}
