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
