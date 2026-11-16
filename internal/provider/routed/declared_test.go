package routed

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func declaredDir(t *testing.T, files map[string]string) *config.Loaded {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "providers"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "providers", name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &config.Loaded{ConfigDir: dir}
}

const houseTOML = `preset = "gateway"
base_url = "http://100.103.40.20:9110/v1"

[oauth]
issuer = "https://auth.example"
client_id = "house"
scopes = ["openid", "groups", "offline_access"]
flow = "device"
`

func TestAProviderFileWithAPresetIsAProvider(t *testing.T) {
	loaded := declaredDir(t, map[string]string{
		"house": houseTOML,
		"keyed": "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n",
		"notes": "base_url = \"http://127.0.0.1:8787/v1\"\n",
	})
	got := RegisterDeclared(loaded)
	if strings.Join(got, ",") != "house,keyed" {
		t.Fatalf("registered %v; a file without a preset must not become a provider", got)
	}
	house := provider.Lookup("house")
	if house == nil || !house.Declared || !house.HasOAuth || house.OAuth == nil {
		t.Fatalf("house = %+v: [oauth] must make hush's token the credential and offer a login", house)
	}
	if keyed := provider.Lookup("keyed"); keyed.HasOAuth || keyed.OAuth != nil {
		t.Fatalf("keyed = %+v: no [oauth], no login", keyed)
	}
	p, err := house.Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil || p == nil {
		t.Fatalf("build: %v", err)
	}
}

func TestTheRegistrationCarriesTheFilesOAuth(t *testing.T) {
	// figaro provider login <name> reads the [oauth] settings off the registration.
	RegisterDeclared(declaredDir(t, map[string]string{"house": houseTOML}))
	got := provider.Lookup("house").OAuth
	if got == nil || got.Issuer != "https://auth.example" || got.ClientID != "house" || got.Flow != "device" || len(got.Scopes) != 3 {
		t.Fatalf("oauth = %+v", got)
	}
}

func TestADeclaredFileCannotReplaceABuiltIn(t *testing.T) {
	before := provider.Lookup("openrouter")
	RegisterDeclared(declaredDir(t, map[string]string{"openrouter": houseTOML}))
	if provider.Lookup("openrouter") != before {
		t.Fatal("providers/openrouter.toml with a preset replaced the built-in")
	}
}

func TestAnUnknownPresetIsNotAProvider(t *testing.T) {
	got := RegisterDeclared(declaredDir(t, map[string]string{"odd": "preset = \"nonesuch\"\nbase_url = \"http://x/v1\"\n"}))
	if len(got) != 0 {
		t.Fatalf("registered %v", got)
	}
}

func TestADeclaredOAuthGatewayNeedsItsCredential(t *testing.T) {
	// The builtin gateway may run keyless; one that declares [oauth] must not
	// silently send no Authorization when the login is missing.
	loaded := declaredDir(t, map[string]string{"house": houseTOML})
	RegisterDeclared(loaded)
	t.Setenv("HOUSE_BASE_URL", "")
	p, err := provider.Lookup("house").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if p.(*Provider).spec.AuthOptional {
		t.Fatal("an [oauth] gateway was built with AuthOptional")
	}
}

// A host that takes a key from the environment (TypeSafe, Experiential Labs)
// is a declared provider like any other; api_key_env names the variable, and
// the resolver reads it through the registration as it does a built-in's.
func TestAPIKeyEnvIsTheRegistrationsVariable(t *testing.T) {
	loaded := declaredDir(t, map[string]string{
		"typesafe": "preset = \"gateway\"\nbase_url = \"https://api.typesafe.ai/v1\"\napi_key_env = \"TYPESAFE_API_KEY\"\n",
		"plain":    "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n",
	})
	RegisterDeclared(loaded)
	if envs := provider.Lookup("typesafe").CredentialEnvs(); strings.Join(envs, ",") != "TYPESAFE_API_KEY" {
		t.Fatalf("typesafe reads %v", envs)
	}
	if envs := provider.Lookup("plain").CredentialEnvs(); len(envs) != 0 {
		t.Fatalf("plain reads %v; no api_key_env, no variable", envs)
	}
}

func TestAServiceTokenAndOAuthAreNotBothTheCredential(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"both": "service_token = \"rou2\"\n" + houseTOML})
	if got := RegisterDeclared(loaded); len(got) != 0 {
		t.Fatalf("registered %v; two credentials for one provider must be refused", got)
	}
}

// A minted token that fails must stop the turn, not send it anonymously.
func TestAServiceTokenMakesTheGatewayRequireAuth(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"svc": "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\nservice_token = \"rou2\"\n"})
	RegisterDeclared(loaded)
	p, err := provider.Lookup("svc").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	if p.(*Provider).spec.AuthOptional {
		t.Fatal("a gateway with a service token treats a missing token as optional")
	}
}

func gatewayAt(t *testing.T, name, body string) *Provider {
	t.Helper()
	loaded := declaredDir(t, map[string]string{name: body})
	RegisterDeclared(loaded)
	p, err := provider.Lookup(name).Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("")})
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

// A gateway that says which endpoints a model serves is listened to without
// the file having to name a catalog.
func TestAGatewayReadsSupportedEndpointsWithoutARoutingTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"data":[{"id":"claude-x","supported_endpoints":["/v1/messages","/chat/completions"]},{"id":"llama"}]}`)
	}))
	defer srv.Close()
	p := gatewayAt(t, "edgegw", "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n")
	for model, want := range map[string]Transport{"claude-x": Messages, "llama": Chat, "unlisted": Chat} {
		got, err := p.route(context.Background(), model)
		if err != nil || got != want {
			t.Errorf("%s: route = %q, %v; want %q", model, got, err, want)
		}
	}
}

// A gateway without /models, or with a broken one, still answers the turn by
// its rules and default, and is not asked again on every turn.
func TestAGatewayWithoutACatalogRoutesByRulesAndDefault(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		var mu sync.Mutex
		hits := map[string]int{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			mu.Lock()
			hits[r.URL.Path]++
			mu.Unlock()
			if r.URL.Path == "/v1/chat/completions" {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chat\"}}]}\n\ndata: [DONE]\n\n")
				return
			}
			w.WriteHeader(status)
		}))
		p := gatewayAt(t, "plaingw", "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n[[routing.rule]]\nmodel = \"claude-*\"\ntransport = \"messages\"\n")

		if got, err := p.route(context.Background(), "claude-x"); err != nil || got != Messages {
			t.Errorf("%d: rule route = %q, %v", status, got, err)
		}
		for i := 0; i < 2; i++ {
			be, aria := store.NewTestAria(t, "d", message.Patch{})
			figLog, _ := be.OpenFigIR(aria)
			_, _ = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
				Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
			}})
			p.translators = func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
			p.SetModel("llama")
			if err := p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &nullBus{}); err != nil {
				t.Fatalf("%d: turn %d failed: %v", status, i, err)
			}
		}
		srv.Close()
		mu.Lock()
		if hits["/v1/models"] != 1 || hits["/v1/chat/completions"] != 2 {
			t.Errorf("%d: hits %v; want one catalog request and two turns", status, hits)
		}
		mu.Unlock()
	}
}

// A gateway that wants static routing, and translation on append, turns the
// catalog off.
func TestCatalogOffKeepsAGatewayStatic(t *testing.T) {
	p := gatewayAt(t, "staticgw", "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n[routing]\ncatalog = \"off\"\n")
	if p.spec.Catalog != "" || !p.CatalogOff() || p.TranslatorChannel() != "staticgw" {
		t.Fatalf("catalog %q, off %v, channel %q", p.spec.Catalog, p.CatalogOff(), p.TranslatorChannel())
	}
	if p := gatewayAt(t, "dynamicgw", "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n"); p.TranslatorChannel() != "" || p.CatalogOff() {
		t.Fatalf("a gateway with its catalog on translated on append under %q (off=%v)", p.TranslatorChannel(), p.CatalogOff())
	}
}

// Defends #118: catalog = "off" never fails listing or calls /models, and
// unconfigured built-in presets (gateway, azure) are omitted from ConfiguredNames.
func TestCatalogOffListsConfiguredModelsAndUnconfiguredPresetsAreOmitted(t *testing.T) {
	t.Setenv("FIGARO_GATEWAY_BASE_URL", "")
	t.Setenv("AZURE_BASE_URL", "")

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	loaded := declaredDir(t, map[string]string{
		"tdf-claude":  "preset = \"gateway\"\nbase_url = \"" + srv.URL + "/v1\"\n[routing]\ncatalog = \"off\"\ndefault = \"messages\"\n[models.\"claude-sonnet-4-6\"]\ncontext = 200000\nmax_output = 64000\n",
		"silent-chat": "preset = \"gateway\"\nbase_url = \"" + srv.URL + "/v1\"\n[routing]\ncatalog = \"off\"\n",
	})
	RegisterDeclared(loaded)

	cfgNames := provider.ConfiguredNames(loaded)
	if slices.Contains(cfgNames, "gateway") || slices.Contains(cfgNames, "azure") {
		t.Fatalf("unconfigured built-in presets appeared in ConfiguredNames: %v", cfgNames)
	}
	if !slices.Contains(cfgNames, "tdf-claude") {
		t.Fatalf("declared provider missing from ConfiguredNames: %v", cfgNames)
	}

	t.Setenv("FIGARO_GATEWAY_BASE_URL", srv.URL+"/v1")
	if !slices.Contains(provider.ConfiguredNames(loaded), "gateway") {
		t.Fatal("gateway with FIGARO_GATEWAY_BASE_URL set must be reported configured")
	}

	pMsg, err := provider.Lookup("tdf-claude").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("")})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pMsg.Models(context.Background())
	if err != nil {
		t.Fatalf("catalog = off failed listing: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "claude-sonnet-4-6" || listed[0].Window("") != 200000 {
		t.Fatalf("listed = %+v, want configured claude-sonnet-4-6", listed)
	}

	pChat, err := provider.Lookup("silent-chat").Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("")})
	if err != nil {
		t.Fatal(err)
	}
	listedChat, err := pChat.Models(context.Background())
	if err != nil || len(listedChat) != 0 {
		t.Fatalf("silent-chat Models = (%+v, %v), want empty without error", listedChat, err)
	}
	if hits != 0 {
		t.Fatalf("catalog = off made %d upstream requests", hits)
	}
}

func TestCredSocketRejectsCoexistenceAndBadFieldsInDeclaredProviders(t *testing.T) {
	cases := map[string]string{
		"cred and oauth":         "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n[oauth]\nissuer = \"https://auth.example\"\nclient_id = \"house\"\n[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"inst\"\n",
		"cred and service_token": "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\nservice_token = \"rou2\"\n[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"inst\"\n",
		"non-abs socket":         "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n[cred]\nsocket = \"relative/inst.sock\"\nname = \"inst\"\n",
		"bad name":               "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"bad/name\"\n",
	}
	for label, doc := range cases {
		t.Run(label, func(t *testing.T) {
			loaded := declaredDir(t, map[string]string{"badcred": doc})
			if got := RegisterDeclared(loaded); len(got) != 0 {
				t.Fatalf("%s: registered %v", label, got)
			}
		})
	}
}

// A gateway with [cred] requires auth, never sends a keyless request when the
// socket fails, and re-fetches once from the socket on an upstream 401.
func TestACredSocketMakesTheGatewayRequireAuthAndRefetchesOn401(t *testing.T) {
	var upstreamHits atomic.Int32
	var seenAuth []string
	var authMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		hdr := r.Header.Get("Authorization")
		authMu.Lock()
		seenAuth = append(seenAuth, hdr)
		authMu.Unlock()
		if hdr != "Bearer fresh-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"revoked"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	sockDir, err := os.MkdirTemp("", "routedcred-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "inst.sock")
	var sockMode atomic.Int32
	var sockHits atomic.Int32
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	credSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sockMode.Load() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"credential_stale","reason":"sign-in needed"}`)
			return
		}
		tok := "stale-tok"
		if sockHits.Add(1) > 1 {
			tok = "fresh-tok"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "inst",
			"token":      tok,
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	})}
	go func() { _ = credSrv.Serve(ln) }()
	t.Cleanup(func() { _ = credSrv.Close() })

	loaded := declaredDir(t, map[string]string{
		"credgw":  "preset = \"gateway\"\nbase_url = \"" + srv.URL + "/v1\"\n[routing]\ncatalog = \"off\"\ndefault = \"chat\"\n[cred]\nsocket = \"" + sock + "\"\nname = \"inst\"\n",
		"gemcred": "preset = \"gateway\"\napi = \"gemini\"\n[cred]\nsocket = \"/run/hush-cred/gem.sock\"\nname = \"gem\"\n",
	})
	if got := RegisterDeclared(loaded); strings.Join(got, ",") != "credgw,gemcred" {
		t.Fatalf("registered = %v", got)
	}
	p, err := provider.Lookup("credgw").Build(provider.BuildContext{
		Loaded:   loaded,
		Resolver: &auth.CredSocket{Socket: sock, Name: "inst"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rp := p.(*Provider)
	if rp.spec.AuthOptional {
		t.Fatal("credgw built with AuthOptional = true")
	}

	gp, err := provider.Lookup("gemcred").Build(provider.BuildContext{
		Loaded:   loaded,
		Resolver: staticToken("tok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gp.(*Provider).spec.AuthOptional || gp.(*Provider).spec.AuthStyle != provider.AuthBearer {
		t.Fatalf("gemcred spec AuthOptional=%v AuthStyle=%v", gp.(*Provider).spec.AuthOptional, gp.(*Provider).spec.AuthStyle)
	}

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}})
	rp.translators = func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	rp.SetModel("llama")
	if err := rp.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &nullBus{}); err == nil || !strings.Contains(err.Error(), "sign-in needed") {
		t.Fatalf("expected Send to fail with sign-in needed when cred socket returns 503, got %v", err)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("sent %d upstream requests despite failed cred socket", upstreamHits.Load())
	}

	sockMode.Store(1)
	if err := rp.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &nullBus{}); err != nil {
		t.Fatalf("expected Send to recover after 401 re-fetch, got %v", err)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if strings.Join(seenAuth, ",") != "Bearer stale-tok,Bearer fresh-tok" {
		t.Fatalf("seenAuth = %v", seenAuth)
	}
}

type nullBus struct{}

func (nullBus) PushDelta(message.Content)                              {}
func (nullBus) PushToolInvokeStart(string, string)                     {}
func (nullBus) PushToolInvokeDelta(string, string)                     {}
func (nullBus) PushToolReady(message.Content)                          {}
func (nullBus) PushMessageEnd(string)                                  {}
func (nullBus) PushFigaro(message.Message, ...provider.AssistantCache) {}
