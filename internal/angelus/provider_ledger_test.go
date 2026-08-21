package angelus

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/logring"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/wirelog"
)

// installRing points the process's log ring at a fresh instance for the
// duration of a test, exactly as otel.Init does in the daemon.
func installRing(t *testing.T) {
	t.Helper()
	ring := logring.New(slog.NewJSONHandler(newDiscard(), nil), 64,
		logring.Any(logring.AtLeast(slog.LevelWarn), logring.WithMessage(wirelog.RoundLog)))
	prevLog := slog.Default()
	slog.SetDefault(slog.New(ring))
	figOtel.SetRecentForTest(ring)
	t.Cleanup(func() {
		slog.SetDefault(prevLog)
		figOtel.SetRecentForTest(nil)
	})
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
func newDiscard() discard                   { return discard{} }

// The whole feature, end to end and through the real handler, because that is
// the path an operator uses at three in the morning: a refusal must arrive
// with the wait the provider asked for.
func TestProviderLedgerHandlerReportsARefusal(t *testing.T) {
	installRing(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("request-id", "req_quota")
		w.Header().Set("anthropic-ratelimit-input-tokens-reset", "2026-08-14T20:00:00Z")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &wirelog.Transport{Inner: http.DefaultTransport}}
	req, err := http.NewRequestWithContext(
		wirelog.WithAria(context.Background(), "94f0752b"),
		http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	h := &handlers{}
	params, _ := json.Marshal(rpc.ProviderLedgerRequest{Aria: "94f0752b"})
	out, err := h.providerLedger(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(rpc.ProviderLedgerResponse)
	if len(got.Rounds) != 1 {
		t.Fatalf("want 1 round for the aria, got %d", len(got.Rounds))
	}
	r := got.Rounds[0]
	if r.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d", r.Status)
	}
	if r.RetryAfterS != 3600 {
		t.Errorf("retry_after_s = %d; this is the field the whole feature exists for", r.RetryAfterS)
	}
	if r.RequestID != "req_quota" {
		t.Errorf("request_id = %q", r.RequestID)
	}
	if r.StartedAtMS == 0 {
		t.Error("started_at_ms not carried to the wire")
	}
	if r.RateLimit["input-tokens-reset"] != "2026-08-14T20:00:00Z" {
		t.Errorf("ratelimit headers lost in the log round-trip: %v", r.RateLimit)
	}
	if r.InFlight {
		t.Error("a completed round-trip must not read as in flight")
	}
}

// The half a log cannot express. An outstanding request is current state, and
// it is the request an incident is actually about.
func TestProviderLedgerShowsOutstandingRequests(t *testing.T) {
	installRing(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req = req.WithContext(wirelog.WithAria(req.Context(), "94f0752b"))
		client := &http.Client{Transport: &wirelog.Transport{Inner: http.DefaultTransport}}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	h := &handlers{}
	params, _ := json.Marshal(rpc.ProviderLedgerRequest{Aria: "94f0752b"})
	deadline := time.After(2 * time.Second)
	for {
		out, _ := h.providerLedger(context.Background(), params)
		got := out.(rpc.ProviderLedgerResponse)
		if len(got.Rounds) == 1 && got.Rounds[0].InFlight {
			break
		}
		select {
		case <-deadline:
			t.Fatal("an outstanding request never appeared in the ledger")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)
	<-done
}

// The aria filter has to actually filter, or "why is 94f0752b stuck" comes
// back as the whole daemon's traffic.
func TestProviderLedgerFiltersByAriaAndLimit(t *testing.T) {
	installRing(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &wirelog.Transport{Inner: http.DefaultTransport}}
	for _, aria := range []string{"aaaa1111", "bbbb2222", "aaaa1111", "aaaa1111"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req = req.WithContext(wirelog.WithAria(req.Context(), aria))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	h := &handlers{}
	params, _ := json.Marshal(rpc.ProviderLedgerRequest{Aria: "aaaa1111"})
	out, _ := h.providerLedger(context.Background(), params)
	got := out.(rpc.ProviderLedgerResponse)
	if len(got.Rounds) != 3 {
		t.Fatalf("aria filter returned %d rounds, want 3", len(got.Rounds))
	}
	if got.Retained != 4 {
		t.Errorf("retained = %d, want 4 - a caller must be able to tell a filter from an empty ring", got.Retained)
	}

	params, _ = json.Marshal(rpc.ProviderLedgerRequest{Aria: "aaaa1111", Limit: 2})
	out, _ = h.providerLedger(context.Background(), params)
	got = out.(rpc.ProviderLedgerResponse)
	if len(got.Rounds) != 2 {
		t.Fatalf("limit ignored: %d rounds", len(got.Rounds))
	}
	if got.Rounds[0].Seq >= got.Rounds[1].Seq {
		t.Error("rounds must come back oldest first")
	}
}

// Telemetry is not installed in every process (the CLI does not init it). A
// diagnostic that panics when diagnostics are off is not a diagnostic.
func TestProviderLedgerToleratesNoRing(t *testing.T) {
	figOtel.SetRecentForTest(nil)
	h := &handlers{}
	out, err := h.providerLedger(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(rpc.ProviderLedgerResponse)
	if got.Retained != 0 {
		t.Errorf("retained = %d with no ring", got.Retained)
	}
}
