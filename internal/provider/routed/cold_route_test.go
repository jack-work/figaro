package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type slowCatalog struct {
	delay  time.Duration
	hold   chan struct{}
	once   sync.Once
	listed atomic.Int32
}

func (g *slowCatalog) release() {
	if g.hold != nil {
		g.once.Do(func() { close(g.hold) })
	}
}

func (g *slowCatalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	switch r.URL.Path {
	case "/v1/models":
		if g.hold != nil {
			select {
			case <-g.hold:
			case <-r.Context().Done():
				return
			}
		}
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

func persistedIR(t *testing.T, name string, refreshed time.Time, ids ...string) form.Snapshot {
	t.Helper()
	f := store.NewMemForm()
	p := apimodels.Provider{IR: apimodels.IRVersion, RefreshedAt: refreshed.UnixMilli()}
	for _, id := range ids {
		p.Place(id, apimodels.Entry{Endpoints: []string{"/chat/completions"}})
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := f.Snapshot()
	if _, _, err := f.ApplyEffect(form.Build(snap, map[string]json.RawMessage{name: raw}, nil), 0); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := f.Snapshot()
	return snapshot
}

func coldGateway(t *testing.T, name string, delay time.Duration, def Transport) (*Provider, *slowCatalog, func(model string) error) {
	t.Helper()
	g := &slowCatalog{delay: delay}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	t.Cleanup(g.release)
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("say something")},
	}}); err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, Spec{Name: name, BaseURL: func() string { return srv.URL + "/v1" }, Catalog: "/models", Default: def, Mapping: copilotShape})
	p.translators = func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
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
	p, g, send := coldGateway(t, "cold-ir", 0, Chat)
	g.hold = make(chan struct{})
	p.seedFromIR(persistedIR(t, "cold-ir", time.Now().Add(-48*time.Hour), "gpt-x"))
	if err := send("gpt-x"); err != nil {
		t.Fatalf("the first turn after a restart did not finish while the upstream catalog was still unanswered: %v", err)
	}
	if g.listed.Load() != 0 {
		t.Fatalf("the catalog answered before it was released (%d lists)", g.listed.Load())
	}
	g.release()
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
	p, g, _ := coldGateway(t, "unknown-slow", 0, "")
	g.hold = make(chan struct{})
	_, err := p.route(context.Background(), "late")
	if err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("route = %v, want the catalog's silence named", err)
	}
}

// The daemon warms each gateway's catalog at start, beside everything else,
// so even an aged persisted catalog is fresh before the first turn needs it.
func TestWarmRefreshesAnAgedCatalogInTheBackground(t *testing.T) {
	p, g, _ := coldGateway(t, "warm", 0, Chat)
	g.hold = make(chan struct{})
	p.seedFromIR(persistedIR(t, "warm", time.Now().Add(-48*time.Hour), "gpt-x"))
	returned := make(chan struct{})
	go func() { p.Warm(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Minute):
		t.Fatal("Warm held its caller while the catalog was unanswered")
	}
	g.release()
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
