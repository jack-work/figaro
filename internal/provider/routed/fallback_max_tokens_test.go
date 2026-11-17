package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// The fallback target's row states its output but not its window; the
// candidate's context length is the only window there is, and the request
// must fit in it.
func TestANativeFallbackClampsTheDefaultToTheCandidateWindow(t *testing.T) {
	const alias, target, capacity = "pair@auto", "vendor/concrete", 50000
	var sent []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.Unmarshal(raw, &body)
		sent = append(sent, body.MaxTokens)
		if len(sent) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			fmt.Fprintf(w, `{"error":{"code":"route_fallback","retry_safe":true,"generation":"g1","route":{"model":%q,"api":%q},"attempt_token":"t"}}`, target, Messages)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, messagesSSE)
	}))
	defer srv.Close()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: prompt("hello")}); err != nil {
		t.Fatal(err)
	}
	p, err := New(Spec{Name: "fallback-max", BaseURL: func() string { return srv.URL }, Default: Responses},
		provider.Knobs{Model: alias}, staticToken("k"),
		func(a, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(a, ns) })
	if err != nil {
		t.Fatal(err)
	}
	window := capacity
	candidate := models.RouteCandidate{Model: target, APIs: []string{string(Messages)}, ContextLength: &window}
	p.cat = newCatalogCache(map[string]models.Entry{
		alias:  {Auto: &models.AutoRoute{Generation: "g1", Candidates: []models.RouteCandidate{candidate}}},
		target: {MaxOutput: 128000},
	})
	in := provider.SendInput{AriaID: aria, FigLog: figLog,
		Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"pair@auto"`)})}
	if err := p.Send(context.Background(), in, &captureBus{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("hits = %d, want the fallback", len(sent))
	}
	if got := sent[1]; got < minDefaultOutput || got > capacity-capacity/20 {
		t.Fatalf("fallback max_tokens = %d, want room for the prompt inside the candidate's %d", got, capacity)
	}
}
