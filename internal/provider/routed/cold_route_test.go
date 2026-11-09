package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type slowCatalog struct {
	delay  time.Duration
	listed atomic.Int32
}

func (g *slowCatalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	switch r.URL.Path {
	case "/v1/models":
		time.Sleep(g.delay)
		g.listed.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-x","supported_endpoints":["/chat/completions"]},{"id":"late","supported_endpoints":["/chat/completions"]}]}`)
	case "/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chatSSE)
	default:
		http.NotFound(w, r)
	}
}

type irBackend struct{ f *store.Form }

func (b irBackend) ModelsForm() (*store.Form, error) { return b.f, nil }

func persistedIR(t *testing.T, name string, refreshed time.Time, ids ...string) irBackend {
	t.Helper()
	f := store.NewMemForm()
	p := apimodels.Provider{IR: apimodels.IRVersion, RefreshedAt: refreshed.UnixMilli(), Models: map[string]apimodels.Entry{}}
	for _, id := range ids {
		p.Models[id] = apimodels.Entry{Endpoints: []string{"/chat/completions"}}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := f.Snapshot()
	if _, _, err := f.ApplyEffect(form.Build(snap, map[string]json.RawMessage{name: raw}, nil), 0); err != nil {
		t.Fatal(err)
	}
	return irBackend{f}
}

func coldGateway(t *testing.T, name string, delay time.Duration, def Transport) (*Provider, *slowCatalog, func(model string) error) {
	t.Helper()
	g := &slowCatalog{delay: delay}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("say something")},
	}}); err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, Spec{Name: name, BaseURL: func() string { return srv.URL + "/v1" }, Catalog: "/models", Default: def, Mapping: copilotShape})
	p.translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	send := func(model string) error {
		p.SetModel(model)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return p.Send(ctx, provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &captureBus{})
	}
	return p, g, send
}

// spain's first turn after every restart waited ~6 s and gluck's ~2.3 s
// (once the full 10 s timeout) before its request was built: route() fetched
// the whole upstream catalog inside the round, because a fresh daemon's
// catalog was empty and an aged one was refetched in line. A restarted
// daemon routes from the catalog it already persisted, and refreshes it
// beside the round.
func TestAColdRouteFromThePersistedCatalogDoesNotWaitForTheUpstream(t *testing.T) {
	p, g, send := coldGateway(t, "cold-ir", 1500*time.Millisecond, Chat)
	p.seedFromIR(persistedIR(t, "cold-ir", time.Now().Add(-48*time.Hour), "gpt-x"))
	start := time.Now()
	if err := send("gpt-x"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the first turn after a restart waited %s for a %s catalog", took, g.delay)
	}
	p.settled(t)
	if g.listed.Load() != 1 {
		t.Fatalf("the aged catalog was listed %d times beside the round, want once", g.listed.Load())
	}
}

// Every figaro builds its own provider, so the catalog one fetched was
// fetched again by the next figaro's first turn. Providers of one gateway
// share it.
func TestASecondFigarosProviderReusesTheCatalog(t *testing.T) {
	p, g, send := coldGateway(t, "shared", 0, "")
	if err := send("gpt-x"); err != nil {
		t.Fatal(err)
	}
	q, err := New(p.spec, p.knobs, p.resolver, p.translators)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.route(context.Background(), "gpt-x"); err != nil {
		t.Fatal(err)
	}
	if g.listed.Load() != 1 {
		t.Fatalf("a second provider of the same gateway listed the catalog again (%d lists)", g.listed.Load())
	}
}

// A model the catalog has never named, on a gateway with no default, still
// waits for the catalog, briefly, and routes once it answers.
func TestAnUnknownModelWithNoDefaultResolvesAfterTheFetch(t *testing.T) {
	p, _, send := coldGateway(t, "unknown-ok", 200*time.Millisecond, "")
	p.seedFromIR(persistedIR(t, "unknown-ok", time.Now(), "gpt-x"))
	if err := send("late"); err != nil {
		t.Fatalf("a model the fresh catalog names did not route: %v", err)
	}
}

func TestAnUnknownModelWithNoDefaultGivesUpWithAReason(t *testing.T) {
	old := routeWait
	routeWait = 100 * time.Millisecond
	t.Cleanup(func() { routeWait = old })
	p, _, _ := coldGateway(t, "unknown-slow", 500*time.Millisecond, "")
	start := time.Now()
	_, err := p.route(context.Background(), "late")
	if err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("route = %v, want the catalog's silence named", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("an unknown model waited %s, past its %s bound", took, routeWait)
	}
}

// The daemon warms each gateway's catalog at start, beside everything else,
// so even an aged persisted catalog is fresh before the first turn needs it.
func TestWarmRefreshesAnAgedCatalogInTheBackground(t *testing.T) {
	p, g, _ := coldGateway(t, "warm", 200*time.Millisecond, Chat)
	p.seedFromIR(persistedIR(t, "warm", time.Now().Add(-48*time.Hour), "gpt-x"))
	start := time.Now()
	p.Warm()
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Fatalf("Warm held its caller %s", took)
	}
	p.settled(t)
	if _, known := p.cat.lookup("late"); !known || g.listed.Load() != 1 {
		t.Fatalf("Warm did not refresh the aged catalog (lists %d)", g.listed.Load())
	}
	p.Warm()
	p.settled(t)
	if g.listed.Load() != 1 {
		t.Fatalf("Warm refreshed a fresh catalog again (lists %d)", g.listed.Load())
	}
}
