package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type movingGateway struct {
	mu        sync.Mutex
	endpoint  string
	modelsErr bool
	chatErr   bool
	hits      []string
}

func (g *movingGateway) set(f func(*movingGateway)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func (g *movingGateway) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.hits...)
}

func (g *movingGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	g.mu.Lock()
	g.hits = append(g.hits, r.URL.Path)
	endpoint, modelsErr, chatErr := g.endpoint, g.modelsErr, g.chatErr
	g.mu.Unlock()
	switch r.URL.Path {
	case "/v1/models":
		if modelsErr {
			http.Error(w, "catalog down", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"gpt-x","supported_endpoints":[%q]}]}`, endpoint)
	case "/v1/chat/completions":
		if chatErr {
			http.Error(w, `{"error":{"code":"unsupported_api_for_model"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chatSSE)
	case "/v1/messages":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, messagesSSE)
	default:
		http.NotFound(w, r)
	}
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func movingProvider(t *testing.T, g *movingGateway) (*Provider, *testClock, func() error) {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	var f File
	doc := fmt.Sprintf("base_url = %q\n[routing]\ncatalog = \"/models\"\n", srv.URL+"/v1")
	if _, err := toml.Decode(doc, &f); err != nil {
		t.Fatal(err)
	}
	spec, err := f.Apply(Spec{Name: "moving"})
	if err != nil {
		t.Fatal(err)
	}
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
	p, err := New(spec, provider.Knobs{Model: "gpt-x", MaxTokens: 64}, &revocable{},
		func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	p.now = clock.now
	send := func() error {
		return p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &captureBus{})
	}
	return p, clock, send
}

func wantHits(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("requests %v, want %v", got, want)
	}
}

// Issue #36: a cached model used to keep its route for the provider's life.
func TestACachedRouteIsRefetchedOnceTheCatalogAges(t *testing.T) {
	g := &movingGateway{endpoint: "/chat/completions"}
	_, clock, send := movingProvider(t, g)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	g.set(func(g *movingGateway) { g.endpoint = "/v1/messages" })
	clock.advance(catalogTTL - time.Minute)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Minute)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	wantHits(t, g.seen(),
		"/v1/models", "/v1/chat/completions",
		"/v1/chat/completions",
		"/v1/models", "/v1/messages")
}

func TestAnAgedCatalogThatCannotBeRefetchedStillRoutes(t *testing.T) {
	g := &movingGateway{endpoint: "/chat/completions"}
	_, clock, send := movingProvider(t, g)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	g.set(func(g *movingGateway) { g.modelsErr = true })
	clock.advance(catalogTTL + time.Minute)
	if err := send(); err != nil {
		t.Fatalf("a stale catalog that cannot be refreshed must keep routing, got %v", err)
	}
	clock.advance(time.Minute)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	wantHits(t, g.seen(),
		"/v1/models", "/v1/chat/completions",
		"/v1/models", "/v1/chat/completions",
		"/v1/chat/completions")
}

// The grok-4.7 failure: the gateway moved a model to another transport and
// the stale route was rejected before any output.
func TestARejectedRouteRetriesOnceOnTheTransportAFreshCatalogNames(t *testing.T) {
	g := &movingGateway{endpoint: "/chat/completions"}
	_, clock, send := movingProvider(t, g)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	g.set(func(g *movingGateway) { g.endpoint = "/v1/messages"; g.chatErr = true })
	clock.advance(time.Minute)
	if err := send(); err != nil {
		t.Fatalf("the retry on the fresh route should have succeeded: %v", err)
	}
	wantHits(t, g.seen(),
		"/v1/models", "/v1/chat/completions",
		"/v1/chat/completions", "/v1/models", "/v1/messages")
}

func TestARejectionTheCatalogDoesNotExplainIsNotRetried(t *testing.T) {
	g := &movingGateway{endpoint: "/chat/completions"}
	_, clock, send := movingProvider(t, g)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	g.set(func(g *movingGateway) { g.chatErr = true })
	clock.advance(time.Minute)
	if err := send(); err == nil {
		t.Fatal("the rejection must surface")
	}
	if err := send(); err == nil {
		t.Fatal("the rejection must surface")
	}
	wantHits(t, g.seen(),
		"/v1/models", "/v1/chat/completions",
		"/v1/chat/completions", "/v1/models",
		"/v1/chat/completions")
}
