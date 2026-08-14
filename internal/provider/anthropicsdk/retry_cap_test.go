package anthropicsdk

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/wirelog"
)

// The bug, in one test: anthropic-sdk-go honors Retry-After verbatim with no
// ceiling, so a long-window 429 ("retry in an hour") became an hour of silence
// inside RequestConfig.Execute. The transport must not let an hour through.
func TestRetryAfterIsClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("request-id", "req_hour")
		w.Header().Set("anthropic-ratelimit-input-tokens-reset", "2026-08-14T20:00:00Z")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &retryCapTransport{Max: 60 * time.Second, Aria: "94f0752b"}}

	ctx, note := withRateLimitNote(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After passed through as %q; the SDK would sleep that long", got)
	}
	// The point of the whole change: an hour-long wait must not be slept
	// through at all. x-should-retry:false is the SDK's own override and it
	// is checked ahead of the status code, so Execute returns immediately and
	// the aria is promptable again.
	if got := resp.Header.Get("x-should-retry"); got != "false" {
		t.Fatalf("x-should-retry = %q; the SDK will nap instead of failing, and the aria stays busy", got)
	}
	n, ok := note.snapshot()
	if !ok {
		t.Fatal("nothing recorded on the note")
	}
	if n.askedFor != time.Hour {
		t.Errorf("askedFor = %v, want 1h (the ORIGINAL must survive for the error message)", n.askedFor)
	}
	if n.clampCount != 1 {
		t.Errorf("clampCount = %d, want 1", n.clampCount)
	}
	if n.requestID != "req_hour" {
		t.Errorf("requestID = %q", n.requestID)
	}
	if n.resetHint != "2026-08-14T20:00:00Z" {
		t.Errorf("resetHint = %q", n.resetHint)
	}
}

// A wait we are willing to serve is served: clamping is a ceiling, not a
// rewrite. Backing off sooner than the server asked would only earn another
// 429.
func TestShortRetryAfterPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &retryCapTransport{Max: 60 * time.Second}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want 5 untouched", got)
	}
	// A short throttle is worth riding out, so we must NOT suppress the retry.
	if got := resp.Header.Get("x-should-retry"); got != "" {
		t.Errorf("x-should-retry = %q; a five second wait should still be retried", got)
	}
}

func TestSuccessfulResponseIsUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600") // nonsense on a 200, but prove we ignore it
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, note := withRateLimitNote(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := (&http.Client{Transport: &retryCapTransport{Max: time.Second}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Retry-After"); got != "3600" {
		t.Errorf("non-transient response was rewritten: %q", got)
	}
	if _, ok := note.snapshot(); ok {
		t.Error("a 200 must not be recorded as a refusal")
	}
}

// The clamp is bounded by our policy, and the SDK's retry budget is ours to
// set, so the worst case is arithmetic we control rather than the provider's
// window length. This test is the arithmetic.
func TestWorstCaseSilentWaitIsBounded(t *testing.T) {
	worst := time.Duration(maxRetries) * maxRetryAfter
	if worst > 5*time.Minute {
		t.Fatalf("a rate-limited turn can stall for %v with no output; that is a hang", worst)
	}
}

// The requirement in one test: after a quota refusal the aria must be
// PROMPTABLE. A turn that sleeps holds the agent loop, so the next message
// queues behind an hour of nothing; a turn that fails returns the agent to
// its inbox immediately.
func TestQuotaRefusalEndsTheTurnAtOnce(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &retryCapTransport{Max: 60 * time.Second}}
	start := time.Now()

	// Drive the SDK's own retry decision, which is the thing that used to
	// sleep. shouldRetry consults x-should-retry before the status code.
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sdkWouldRetry(resp) {
		t.Fatal("the SDK would retry this, and its delay is the server's hour")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v; a quota refusal must return at once", elapsed)
	}
	if attempts != 1 {
		t.Errorf("made %d attempts; a spent quota is not retried", attempts)
	}
}

// sdkWouldRetry mirrors requestconfig.shouldRetry for the part we override.
// If the SDK ever stops honoring x-should-retry this test fails, which is
// exactly when we would want to know.
func sdkWouldRetry(res *http.Response) bool {
	switch res.Header.Get("x-should-retry") {
	case "true":
		return true
	case "false":
		return false
	}
	return res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
}

func TestAnnotateRateLimitNamesTheWait(t *testing.T) {
	_, note := withRateLimitNote(context.Background())
	note.record(429, time.Hour, "req_hour", "2026-08-14T20:00:00Z", true)

	err := annotateRateLimit(errors.New("anthropic: rate_limit_error (429)"), note)
	msg := err.Error()
	for _, want := range []string{"1h0m0s", "resets 2026-08-14T20:00:00Z", "req_hour",
		"the aria is free", "fork to a different model"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q:\n  %s", want, msg)
		}
	}
}

func TestAnnotateRateLimitLeavesOrdinaryErrorsAlone(t *testing.T) {
	_, note := withRateLimitNote(context.Background())
	in := errors.New("anthropic: invalid_request_error: model not found (400)")
	if got := annotateRateLimit(in, note); got.Error() != in.Error() {
		t.Errorf("rewrote a non-rate-limit error: %v", got)
	}
	if annotateRateLimit(nil, note) != nil {
		t.Error("nil error must stay nil")
	}
}

// Ordering matters. The round-trip record must carry the header the SERVER
// sent, not our clamp: a diagnostic that reported our own policy back to us
// would be worthless, and the original number is the one that tells an
// operator "this is a usage window, wait for the reset".
func TestTheRecordKeepsTheOriginalRetryAfter(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// The cap transport sits ABOVE wirelog, so wirelog records the response
	// before the clamp is applied.
	client := &http.Client{Transport: &retryCapTransport{
		Inner: &wirelog.Transport{Inner: http.DefaultTransport},
		Max:   60 * time.Second,
	}}
	ctx, _ := withRateLimitNote(wirelog.WithAria(context.Background(), "94f0752b"))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := logs.String()
	if !strings.Contains(got, `"retry_after_s":3600`) {
		t.Errorf("record must keep what the SERVER said, not our clamp:\n%s", got)
	}
	if !strings.Contains(got, `"aria":"94f0752b"`) {
		t.Errorf("record not attributed to the aria:\n%s", got)
	}
}

func TestTransientStatuses(t *testing.T) {
	for _, code := range []int{429, 500, 502, 503, 529} {
		if !transientStatus(code) {
			t.Errorf("%d should be transient", code)
		}
	}
	for _, code := range []int{200, 400, 401, 404} {
		if transientStatus(code) {
			t.Errorf("%d should not be transient", code)
		}
	}
}
