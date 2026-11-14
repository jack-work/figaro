package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAutoResponsesLimitsUseTheMinimumAndClearWhenUnknown(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responsesSSE)
	}))
	defer srv.Close()
	backend, id := store.NewTestAria(t, "auto-limits", message.Patch{})
	log, err := backend.OpenFigIR(id)
	require.NoError(t, err)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("a prompt that exceeds a one token window")}}})
	require.NoError(t, err)
	p, err := New(Spec{Name: "auto-limits", BaseURL: func() string { return srv.URL }, Default: Responses}, provider.Knobs{Model: "auto", MaxTokens: 64}, staticToken("unused"), func(aria, ns string) (store.Log[[]json.RawMessage], error) { return backend.OpenTranslator(aria, ns) })
	require.NoError(t, err)
	tiny := 1
	auto := &models.AutoRoute{ContextStatus: "known", Candidates: []models.RouteCandidate{{Model: "vendor/model", APIs: []string{"responses"}, ContextLength: &tiny}}}
	p.cat = newCatalogCache(map[string]models.Entry{"auto": {Auto: auto, Context: models.Context{Default: 1000000, Long: 2000000}}})
	in := provider.SendInput{AriaID: id, FigLog: log, Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"auto"`)}), MaxTokens: 64}
	err = p.Send(context.Background(), in, &captureBus{})
	require.ErrorContains(t, err, "exceeds")
	require.Zero(t, calls)
	auto.ContextStatus = "unknown"
	auto.Candidates[0].ContextLength = nil
	require.NoError(t, p.Send(context.Background(), in, &captureBus{}))
	require.Equal(t, 1, calls)
}
