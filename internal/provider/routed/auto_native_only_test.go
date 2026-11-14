package routed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
)

func TestAutoExplicitRulesCannotReachNonnativeBackends(t *testing.T) {
	for _, transport := range []Transport{Chat, ResponsesWS} {
		t.Run(string(transport), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.Error(w, "unexpected", 401) }))
			defer srv.Close()
			credentials := &rotatingNativeCredential{}
			p, err := New(Spec{Name: "native-only", BaseURL: func() string { return srv.URL }, Rules: []Rule{{Model: "pair@auto", Transport: transport}}}, provider.Knobs{}, credentials, nil)
			if err != nil {
				t.Fatal(err)
			}
			p.cat = newCatalogCache(map[string]models.Entry{"pair@auto": {Auto: &models.AutoRoute{Generation: "g1"}}})
			err = p.Send(context.Background(), provider.SendInput{Snapshot: form.FromValue(form.NewValue([]byte(`{"system":{"model":"pair@auto"}}`)))}, &captureBus{})
			if err == nil || !strings.Contains(err.Error(), "require native HTTP") {
				t.Fatalf("err=%v", err)
			}
			if requests != 0 || credentials.calls != 0 {
				t.Fatalf("requests=%d credential resolutions=%d", requests, credentials.calls)
			}
		})
	}
}
