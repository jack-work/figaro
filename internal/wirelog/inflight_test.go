package wirelog

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureLog installs a JSON slog handler for the duration of a test and
// returns the buffer. The round-trip record IS the ledger now, so asserting
// on it is asserting on the feature.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func newClient() *http.Client {
	return &http.Client{Transport: &Transport{Inner: http.DefaultTransport}}
}

// A 429 is the single most important thing a provider ever says, and figaro
// recorded neither the refusal nor the wait the server asked for. This is the
// regression test for a 77-minute silent stall.
func TestRefusalIsLoggedWithTheWaitItAskedFor(t *testing.T) {
	logs := captureLog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("request-id", "req_test_429")
		w.Header().Set("anthropic-ratelimit-input-tokens-remaining", "0")
		w.Header().Set("anthropic-ratelimit-input-tokens-reset", "2026-08-14T20:00:00Z")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(
		WithAria(context.Background(), "94f0752b"),
		http.MethodPost, srv.URL, strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := logs.String()
	if !strings.Contains(got, RoundLog) {
		t.Fatalf("no round-trip record was written:\n%s", got)
	}
	for _, want := range []string{
		`"level":"WARN"`,                          // a refusal is not routine
		`"aria":"94f0752b"`,                       // which conversation
		`"status":429`,                            // what happened
		`"retry_after_s":3600`,                    // the number that explains the hang
		`"request_id":"req_test_429"`,             // what to quote to support
		`"ratelimit_input_tokens_remaining":"0"`,  // WHICH limit
		`"ratelimit_input_tokens_reset":"2026-08`, // and when it clears
	} {
		if !strings.Contains(got, want) {
			t.Errorf("record missing %s:\n%s", want, got)
		}
	}
}

// A healthy call is recorded too, at INFO. Without it there is nothing to
// compare the refusal against: "this used to take 11s and 712 KB" is half the
// diagnosis.
func TestSuccessIsRecordedAtInfo(t *testing.T) {
	logs := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := strings.Repeat("x", 4096)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := logs.String()
	if !strings.Contains(got, `"level":"INFO"`) || !strings.Contains(got, `"status":200`) {
		t.Errorf("healthy round-trip not recorded at INFO:\n%s", got)
	}
	// req_bytes used to be len(bodyBytes), and bodyBytes is only materialized
	// when dumping to disk, so in normal operation it was always 0. "This
	// request was 700 KB" is what you want to know about a refused turn.
	if !strings.Contains(got, `"req_bytes":4096`) {
		t.Errorf("req_bytes not taken from ContentLength:\n%s", got)
	}
}

// The one thing a log cannot express is "still running". A record is a point
// in time; an outstanding request is current state, and it is precisely what
// tracing cannot show either, since spans export on end.
func TestOutstandingRequestIsVisibleBeforeItReturns(t *testing.T) {
	captureLog(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req = req.WithContext(WithAria(req.Context(), "abcd1234"))
		if resp, err := newClient().Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	deadline := time.After(2 * time.Second)
	for {
		out := Outstanding()
		if len(out) == 1 && out[0].Aria == "abcd1234" {
			if out[0].Age() < 0 {
				t.Fatal("negative age")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("outstanding request never appeared: %+v", out)
		case <-time.After(5 * time.Millisecond):
		}
	}

	close(release)
	<-done

	if out := Outstanding(); len(out) != 0 {
		t.Fatalf("request stayed outstanding after returning: %+v", out)
	}
}

// A transport error must not strand an entry in the in-flight map. A leak
// here would read as a permanently hung request, which is worse than no
// diagnostic at all.
func TestTransportErrorClearsInFlight(t *testing.T) {
	logs := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing is listening now

	req, _ := http.NewRequest(http.MethodGet, addr, nil)
	if resp, err := newClient().Do(req); err == nil {
		resp.Body.Close()
		t.Skip("expected a connection failure")
	}
	if out := Outstanding(); len(out) != 0 {
		t.Fatalf("in-flight entry leaked on transport error: %+v", out)
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("a failed round-trip should be recorded at WARN:\n%s", logs.String())
	}
}

func TestRetryAfterParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"60", time.Minute},
		{" 90 ", 90 * time.Second},
		{"-1", 0},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0}, // HTTP-date form is not honored
	} {
		h := http.Header{}
		if tc.in != "" {
			h.Set("Retry-After", tc.in)
		}
		if got := RetryAfter(h); got != tc.want {
			t.Errorf("RetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The vendor prefix is stripped so the same key means the same thing across
// providers.
func TestRateLimitHeadersAreNormalized(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-input-tokens-remaining", "0")
	h.Set("x-ratelimit-requests-limit", "50")
	h.Set("content-type", "application/json")

	got := rateLimitHeaders(h)
	if got["input-tokens-remaining"] != "0" || got["requests-limit"] != "50" {
		t.Errorf("normalization failed: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("harvested a non-ratelimit header: %v", got)
	}
}

// Attribution used to ride on the byte-dump switch, so unless someone had set
// figaro_wire_dir every round-trip was anonymous.
func TestWithAriaDoesNotEnableDumping(t *testing.T) {
	ctx := WithAria(context.Background(), "94f0752b")
	c, ok := cfgFromContext(ctx)
	if !ok || c.aria != "94f0752b" {
		t.Fatalf("aria not carried: %+v", c)
	}
	if c.dir != "" {
		t.Errorf("WithAria must not turn on byte dumps, got dir=%q", c.dir)
	}
	c2, _ := cfgFromContext(WithLogging(ctx, "94f0752b", t.TempDir()))
	if c2.aria != "94f0752b" || c2.dir == "" {
		t.Errorf("WithLogging lost state: %+v", c2)
	}
}
