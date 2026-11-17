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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// A gateway that lists <model>@auto without rou2_auto served it before
// v0.69.4; a patch release must not start refusing it (#111).
func TestAListedAutoRowWithoutRou2AutoIsServed(t *testing.T) {
	var reached atomic.Int32
	var sawModel atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		body, _ := io.ReadAll(r.Body)
		sawModel.Store(strings.Contains(string(body), `"legacy@auto"`))
		http.Error(w, "stop here", http.StatusTeapot)
	}))
	defer srv.Close()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}}); err != nil {
		t.Fatal(err)
	}
	p, err := New(Spec{Name: "legacy-auto", BaseURL: func() string { return srv.URL }, Default: Chat},
		provider.Knobs{Model: "legacy@auto", MaxTokens: 64}, staticToken("k"),
		func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
	if err != nil {
		t.Fatal(err)
	}
	p.cat = newCatalogCache(map[string]models.Entry{"legacy@auto": {Name: "legacy@auto"}})
	in := provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64,
		Snapshot: form.FromValue(form.NewValue([]byte(`{"system":{"model":"legacy@auto"}}`)))}
	err = p.Send(context.Background(), in, &captureBus{})
	if err != nil && strings.Contains(err.Error(), "catalog entry is unavailable") {
		t.Fatalf("listed @auto row refused: %v", err)
	}
	if reached.Load() == 0 || !sawModel.Load() {
		t.Fatalf("request never reached the gateway with the model: reached=%d err=%v", reached.Load(), err)
	}
	if !provider.ReplayForbidden(err) {
		t.Fatalf("an @auto failure must still forbid replay: %v", err)
	}
}

func autoRefusalProvider(t *testing.T, model string, rows map[string]models.Entry) (*Provider, provider.SendInput, *atomic.Int32) {
	t.Helper()
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			reached.Add(1)
		}
		http.Error(w, "stop here", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}}); err != nil {
		t.Fatal(err)
	}
	p, err := New(Spec{Name: "auto-refusal", BaseURL: func() string { return srv.URL }, Default: Chat},
		provider.Knobs{Model: model, MaxTokens: 64}, staticToken("k"),
		func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
	if err != nil {
		t.Fatal(err)
	}
	p.cat = newCatalogCache(rows)
	raw, _ := json.Marshal(model)
	in := provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64,
		Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": raw})}
	return p, in, &reached
}

func TestAnAutoAliasTheGatewayDoesNotListIsRefused(t *testing.T) {
	p, in, reached := autoRefusalProvider(t, "gone@auto", map[string]models.Entry{"other": {Name: "other"}})
	err := p.Send(context.Background(), in, &captureBus{})
	if err == nil || !strings.Contains(err.Error(), "auto route catalog entry is unavailable") {
		t.Fatalf("an unlisted @auto alias went out: %v", err)
	}
	if reached.Load() != 0 {
		t.Fatalf("the gateway was reached %d times", reached.Load())
	}
}

// A row that lost rou2_auto since this process routed it is a degraded
// gateway, not a v0.69.3 one.
func TestAnAutoAliasThatLostItsRouteIsRefused(t *testing.T) {
	auto := &models.AutoRoute{Generation: "g1", Candidates: []models.RouteCandidate{{Model: "vendor/concrete", APIs: []string{string(Messages)}}}}
	p, in, reached := autoRefusalProvider(t, "seen@auto", map[string]models.Entry{"seen@auto": {Name: "seen@auto", Auto: auto}})
	_ = p.Send(context.Background(), in, &captureBus{})
	p.cat = newCatalogCache(map[string]models.Entry{"seen@auto": {Name: "seen@auto"}})
	err := p.Send(context.Background(), in, &captureBus{})
	if err == nil || !strings.Contains(err.Error(), "auto route catalog entry is unavailable") {
		t.Fatalf("a listed row that lost rou2_auto went out as a plain model: %v", err)
	}
	if reached.Load() != 0 {
		t.Fatalf("the gateway was reached %d times", reached.Load())
	}
}
