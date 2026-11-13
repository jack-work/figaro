package angelus_test

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

const svcPolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.narrow-decide]
permissions = [PERMS]

[[bind]]
role = "owner"
principals = ["user:UID", "user:admin@auth.example.com"]

[[bind]]
role = "narrow-decide"
principals = ["service:client@auth.example.com"]
`

type svcRig struct {
	t      *testing.T
	a      *angelus.Angelus
	path   string
	policy string
	owned  string
	host   *countingHost
	served []string
}

// svcEndpoint is a web endpoint with the client's service principal bound to
// the given permissions, a decision model that counts what reaches it, and
// one aria the owner made.
func svcEndpoint(t *testing.T, permissions ...string) *svcRig {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	decides := "[system.decide]\nprovider = \"mock\"\nmodel = \"decision-1\"\n"
	for name, body := range map[string]string{
		"mock":          "[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n" + decides,
		"narrow-decide": decides,
		"narrow-group":  decides,
		"narrow":        decides,
	} {
		require.NoError(t, os.WriteFile(dir+"/outfits/"+name+".toml", []byte(body), 0o600))
	}
	r := &svcRig{t: t, policy: filepath.Join(dir, "policy.toml"), host: newCountingHost(t)}
	r.grant(permissions...)
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte(`default_outfit = "mock"
[authz]
policy = "`+r.policy+`"
[authz.upstream]
issuer = "https://auth.example.com"
[authz.upstream.services]
client = "service"
`), 0o600))
	a, acli, ctx, hs := daemonWithProvider(t, dir, nil, func() provider.Provider { return &mockProviderForIntegration{} }, decideThrough(r.host))
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	r.a, r.owned = a, created.FigaroID
	for m := range hs.Map {
		r.served = append(r.served, m)
	}
	slices.Sort(r.served)

	loaded, err := config.Load(dir)
	require.NoError(t, err)
	r.path = filepath.Join(filepath.Dir(a.SocketPath), "web.sock")
	u, err := authz.NewUpstream(loaded.Config.Authz.Upstream, r.path, a.SocketPath, nil)
	require.NoError(t, err)
	ln, err := angelus.ListenUnix(r.path, 0o660)
	require.NoError(t, err)
	go a.ServeWeb(ctx, ln, angelus.Web{Admit: u.AdmitRequest})
	return r
}

func (r *svcRig) grant(permissions ...string) {
	r.t.Helper()
	quoted := make([]string, len(permissions))
	for i, p := range permissions {
		quoted[i] = strconv.Quote(p)
	}
	text := strings.NewReplacer("UID", strconv.Itoa(os.Getuid()), "PERMS", strings.Join(quoted, ", ")).Replace(svcPolicy)
	require.NoError(r.t, os.WriteFile(r.policy, []byte(text), 0o600))
	if r.a != nil {
		require.NoError(r.t, r.a.Policies.Reload(r.policy))
	}
}

func (r *svcRig) client() *edgeClient {
	r.t.Helper()
	c, _, err := forge(r.t, r.path, http.Header{"Remote-User": {"client"}, "Remote-Principal": {"service"}, "Remote-Client": {"client"}})
	require.NoError(r.t, err)
	return c
}

// reached reports whether a decide call got past the policy to the
// decision model.
func (r *svcRig) reached(c *edgeClient, params map[string]any) (bool, rpcReply) {
	r.t.Helper()
	before := r.host.hits.Load()
	reply := c.call(rpc.MethodDecide, params)
	return r.host.hits.Load() > before, reply
}

func ask(extra map[string]any) map[string]any {
	params := map[string]any{"question": "go?"}
	for k, v := range extra {
		params[k] = v
	}
	return params
}

// The client's service principal holds decide:outfit:narrow-decide and nothing else:
// it decides in that outfit, and every other outfit, the default, an
// override of the decision model, and an aria are refused before the model
// is asked.
func TestDecideOnOneOutfitReachesThatOutfitAndNoOther(t *testing.T) {
	r := svcEndpoint(t, "decide:outfit:narrow-decide")
	c := r.client()

	ok, reply := r.reached(c, ask(map[string]any{"outfit": "narrow-decide"}))
	require.True(t, ok, "the client's own outfit did not reach the model: %+v", reply.Error)

	for name, params := range map[string]map[string]any{
		"the group outfit":       {"outfit": "narrow-group"},
		"the default outfit":     {},
		"the default, by name":   {"outfit": "default"},
		"a provider override":    {"outfit": "narrow-decide", "provider": "mock"},
		"a model override":       {"outfit": "narrow-decide", "model": "decision-2"},
		"an api override":        {"outfit": "narrow-decide", "api": "chat"},
		"an aria":                {rpc.AriaKey: r.owned},
		"an aria and the outfit": {rpc.AriaKey: r.owned, "outfit": "narrow-decide"},
		"a near-miss name":       {"outfit": "narrow-decide "},
		"a prefix of the name":   {"outfit": "narrow"},
	} {
		ok, reply := r.reached(c, ask(params))
		require.False(t, ok, "%s reached the model", name)
		require.NotNil(t, reply.Error, name)
	}
}

// Every route the endpoint serves, called by the client with an aria the owner
// holds: hello answers, the row-filtered listings (and the client's own api
// keys, of which it has none) answer with nothing of anyone else's, and
// everything else is refused. The canary widens the grant
// with read:* and the same calls start answering, so the refusals above
// come from the grant and not from calls that could never succeed.
func TestDecideOnOneOutfitIsRefusedOnEveryOtherRoute(t *testing.T) {
	r := svcEndpoint(t, "decide:outfit:narrow-decide")
	c := r.client()
	require.Greater(t, len(r.served), 60, "the endpoint serves %d routes; the sweep is broken", len(r.served))

	answered := func(m string) rpcReply {
		return c.call(m, onAria(r.owned, map[string]any{"text": "witness-svc", "question": "go?", "version": rpc.ProtocolVersion}))
	}
	rows := map[string]bool{rpc.MethodList: true, rpc.MethodNodeResolve: true, rpc.MethodTroupeAPIKeyList: true}
	for _, m := range r.served {
		if _, ok := rpc.VerbOf(m); !ok {
			t.Errorf("%s is served with no verb", m)
		}
		if m == rpc.MethodDecide {
			continue
		}
		reply := answered(m)
		switch {
		case m == rpc.MethodHello:
			require.Nil(t, reply.Error, m)
		case rows[m]:
			if reply.Error == nil {
				require.NotContains(t, string(reply.Result), r.owned, "%s showed the client the owner's aria", m)
				require.NotContains(t, string(reply.Result), "user:", "%s showed the client another principal's rows", m)
			}
		default:
			require.NotNil(t, reply.Error, "%s answered the client: %s", m, reply.Result)
		}
	}
	require.Zero(t, r.host.hits.Load(), "the sweep reached the decision model")

	owner, _, err := forge(t, r.path, http.Header{"Remote-User": {"admin"}})
	require.NoError(t, err)
	require.Nil(t, owner.call(rpc.MethodContext, onAria(r.owned, nil)).Error, "the owner's aria did not survive the sweep")

	r.grant("decide:outfit:narrow-decide", "read:*")
	for _, m := range []string{rpc.MethodContext, rpc.MethodForm, rpc.MethodIR, rpc.MethodLineage, rpc.MethodRead} {
		require.Nil(t, answered(m).Error, "canary: %s with read:* still refused", m)
	}
}

// The canary for the outfit check: widened to decide:*, the same calls the
// narrow grant refused reach the model, and send:* (what an aria-less
// decide needed before the decide verb) still does.
func TestAWiderDecideGrantReachesWhatTheNarrowOneDoesNot(t *testing.T) {
	for _, wide := range []string{"decide:*", "send:*"} {
		r := svcEndpoint(t, wide)
		c := r.client()
		for name, params := range map[string]map[string]any{
			"the group outfit":   {"outfit": "narrow-group"},
			"the default outfit": {},
			"a model override":   {"outfit": "narrow-decide", "model": "decision-2"},
		} {
			ok, reply := r.reached(c, ask(params))
			require.True(t, ok, "%s: %s did not reach the model: %+v", wide, name, reply.Error)
		}
	}
}

// A decide bound to an aria reads its transcript and form, so it needs read
// on the aria as well as send: send alone is not found, send and read
// decide on it as send did before the decide verb.
func TestAnAriaBoundDecideNeedsReadAndSend(t *testing.T) {
	r := svcEndpoint(t, "decide:outfit:narrow-decide")
	c := r.client()
	r.grant("send:aria:" + r.owned)
	ok, reply := r.reached(c, ask(map[string]any{rpc.AriaKey: r.owned}))
	require.False(t, ok, "send without read decided on the aria")
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "not found")
	r.grant("send:aria:"+r.owned, "read:aria:"+r.owned)
	ok, reply = r.reached(c, ask(map[string]any{rpc.AriaKey: r.owned}))
	require.True(t, ok, "send and read on the aria: %+v", reply.Error)
	r.grant("read:aria:" + r.owned)
	ok, _ = r.reached(c, ask(map[string]any{rpc.AriaKey: r.owned}))
	require.False(t, ok, "read without send decided on the aria")
}

// An override of the decision model is checked on the daemon whatever else
// the decide names: send and read on an aria decide on it but may not pick
// its model, and decide:* may.
func TestAnOverrideOnAnAriaBoundDecideNeedsTheDaemon(t *testing.T) {
	overrides := map[string]map[string]any{
		"a provider override": {"provider": "mock"},
		"a model override":    {"model": "decision-2"},
		"an api override":     {"api": "openai"},
	}
	r := svcEndpoint(t, "decide:outfit:narrow-decide")
	c := r.client()
	r.grant("send:aria:"+r.owned, "read:aria:"+r.owned)
	ok, reply := r.reached(c, ask(map[string]any{rpc.AriaKey: r.owned}))
	require.True(t, ok, "control: send and read on the aria, no override: %+v", reply.Error)
	for name, o := range overrides {
		o[rpc.AriaKey] = r.owned
		ok, reply := r.reached(c, ask(o))
		require.False(t, ok, "%s on the aria with send:aria reached the model", name)
		require.NotNil(t, reply.Error, name)
	}
	r.grant("decide:*", "read:aria:"+r.owned)
	for name, o := range overrides {
		ok, reply := r.reached(c, ask(o))
		require.True(t, ok, "canary: %s on the aria with decide:* did not reach the model: %+v", name, reply.Error)
	}
}
