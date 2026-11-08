package angelus_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/methods"
	"github.com/jack-work/figaro/internal/provider"
)

type turnSignal struct {
	mockProviderForIntegration
	sent chan struct{}
}

func (p *turnSignal) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	err := p.mockProviderForIntegration.Send(ctx, in, bus)
	p.sent <- struct{}{}
	return err
}

func outOfCredits(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"You have no credits remaining."}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The 2026-11-06 shape: the decision model answers 429 on every inquiry,
// and each inquiry waited for it to say so.
func TestADecisionRouteOutOfCreditsIsAskedThreeTimesNotOnEveryInquiry(t *testing.T) {
	srv, hits := outOfCredits(t)
	p := &turnSignal{sent: make(chan struct{}, 1)}
	r := newRigProviding(t, defaultPolicy, "", func(string, provider.Knobs) (provider.Provider, error) { return p, nil },
		func(c *angelus.ServerConfig) {
			c.Decider = decide.New(func(string) (decide.Target, error) { return decide.Target{BaseURL: srv.URL}, nil })
		})

	set := map[string]json.RawMessage{
		"system.methods":   json.RawMessage(`{"mood": {"key": "mood", "context": "{{ inquiry }}", "options": ["calm", "angry"], "default": "calm"}}`),
		decide.ProviderKey: json.RawMessage(`"rou2"`),
		decide.ModelKey:    json.RawMessage(`"jev-latest"`),
	}
	patch := form.Build(form.Snapshot{}, set, nil)
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: r.cwd, Patch: &patch})
	out, err := r.h[rpc.MethodCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(rpc.CreateResponse).FigaroID

	const inquiries = 6
	for i := range inquiries {
		if _, err := r.h[rpc.MethodQua](asOwner(), withAria(t, id, rpc.QuaRequest{Text: "hello " + string(rune('a'+i))})); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.sent:
		case <-time.After(10 * time.Second):
			t.Fatalf("turn %d did not finish", i)
		}
	}

	if got := hits.Load(); got != methods.RestAfter {
		t.Fatalf("the out-of-credits route was asked %d times over %d inquiries, want %d", got, inquiries, methods.RestAfter)
	}
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup("mood"); v == nil || *v != "calm" {
		t.Fatalf("mood = %v, want the method's default", v)
	}

	led, err := r.h[rpc.MethodProviderLedger](asOwner(), nil)
	if err != nil {
		t.Fatal(err)
	}
	routes := led.(rpc.ProviderLedgerResponse).DecideRoutes
	if len(routes) != 1 || routes[0].Route != "rou2/jev-latest" || routes[0].Failures != methods.RestAfter ||
		!strings.Contains(routes[0].LastError, "no credits remaining") {
		t.Fatalf("doctor's view of the route: %+v", routes)
	}
	if next := time.UnixMilli(routes[0].NextTryMS); time.Until(next) <= 0 || time.Until(next) > methods.FirstRest {
		t.Fatalf("next try %s", next)
	}
}
