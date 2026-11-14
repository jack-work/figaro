package routed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestUnknownAutoContextRequiresAnAbsoluteSuccessionFallback(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "reached upstream", http.StatusBadGateway)
	}))
	defer srv.Close()
	p, err := New(Spec{Name: "unknown-succession", BaseURL: func() string { return srv.URL }, Default: Responses}, provider.Knobs{Model: "auto"}, staticToken("unused"), nil)
	require.NoError(t, err)
	p.cat = newCatalogCache(map[string]models.Entry{"auto": {Endpoints: []string{"/responses"}, Auto: &models.AutoRoute{ContextStatus: "unknown", Candidates: []models.RouteCandidate{{Model: "vendor/model", APIs: []string{"responses"}}}}}})
	in := provider.SendInput{Snapshot: form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"auto"`), "system.retirement": json.RawMessage(`{"context_threshold":"60%","action":"succeed"}`)})}
	err = p.Send(context.Background(), in, &captureBus{})
	require.ErrorContains(t, err, "fallback_tokens")
	require.Zero(t, calls)
}
