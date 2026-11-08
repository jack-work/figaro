package decide

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func engineAnswering(t *testing.T, status int, body string) *Engine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return at(srv.URL)
}

func askThrough(t *testing.T, e *Engine, src Source) error {
	t.Helper()
	_, err := e.Ask(context.Background(), src, Question{Ask: "which?", Options: []Option{{Label: "a"}, {Label: "b"}}})
	return err
}

func TestARefusingRouteIsAnUpstreamFailure(t *testing.T) {
	src := Source{Defaults: defaultsOf(t, "rou2", "jev-latest")}
	for name, e := range map[string]*Engine{
		"429":       engineAnswering(t, http.StatusTooManyRequests, `{"error":{"message":"You have no credits remaining."}}`),
		"no answer": engineAnswering(t, http.StatusOK, `{"answers":{}}`),
		"no target": New(func(string) (Target, error) { return Target{}, errors.New("not logged in") }),
	} {
		if err := askThrough(t, e, src); !errors.Is(err, ErrUpstream) {
			t.Errorf("%s: %v is not marked as the route failing", name, err)
		}
	}
}

func TestARefusalBeforeTheRouteIsNotTheRoutes(t *testing.T) {
	e := engineAnswering(t, http.StatusTooManyRequests, `{}`)
	for name, src := range map[string]Source{
		"box policy": {Defaults: defaultsOf(t, "rou2", "jev-latest"), Allowed: func(string) error { return errors.New("not allowed") }},
		"no route":   {},
	} {
		if err := askThrough(t, e, src); err == nil || errors.Is(err, ErrUpstream) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheRouteNamesProviderModelAndBase(t *testing.T) {
	src := Source{BoundForm: boundFormOf(t, map[string]string{BaseURLKey: `"http://127.0.0.1:8787"`}), Defaults: defaultsOf(t, "rou2", "gpt-6-luna")}
	if got, err := src.Route(); err != nil || got != "rou2/gpt-6-luna at http://127.0.0.1:8787" {
		t.Fatalf("route %q, %v", got, err)
	}
	if _, err := (Source{}).Route(); err == nil {
		t.Fatal("a source with no decision settings has no route")
	}
}
