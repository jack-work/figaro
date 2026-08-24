package cli

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

// The verdict is the product. A table of HTTP statuses is evidence; an
// operator staring at a silent aria needs to be told, in words, that the
// provider refused them and for how long.
func TestProviderTroubleSummaryNamesTheQuotaWait(t *testing.T) {
	rounds := []rpc.ProviderRound{
		{Seq: 1, Aria: "94f0752b", Status: 200, DurationMS: 11000},
		{Seq: 2, Aria: "94f0752b", Status: 429, DurationMS: 1060, RetryAfterS: 3600,
			RateLimit: map[string]string{"input-tokens-reset": "2026-08-14T20:00:00Z"}},
	}
	out := captureStdout(t, func() { summarizeProviderTrouble(rounds) })

	for _, want := range []string{"1 of 2", "refused for quota", "1h0m0s", "usage window", "resets 2026-08-14T20:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

// A healthy ledger says nothing. Diagnostics that always print something
// train people to stop reading them.
func TestProviderTroubleSummarySilentWhenHealthy(t *testing.T) {
	rounds := []rpc.ProviderRound{{Seq: 1, Status: 200, DurationMS: 900}}
	if out := captureStdout(t, func() { summarizeProviderTrouble(rounds) }); out != "" {
		t.Errorf("expected silence for a healthy ledger, got:\n%s", out)
	}
}

func TestProviderTroubleSummaryCountsInFlight(t *testing.T) {
	rounds := []rpc.ProviderRound{
		{Seq: 1, Aria: "94f0752b", InFlight: true, StartedAtMS: time.Now().Add(-4 * time.Minute).UnixMilli()},
	}
	out := captureStdout(t, func() { summarizeProviderTrouble(rounds) })
	if !strings.Contains(out, "still in flight") {
		t.Errorf("an outstanding request must be named:\n%s", out)
	}
}

func TestShortEndpointKeepsThePath(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.anthropic.com/v1/messages": "/v1/messages",
		"https://api.anthropic.com":             "api.anthropic.com",
		"/v1/messages":                          "/v1/messages",
	} {
		if got := shortEndpoint(in); got != want {
			t.Errorf("shortEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanMillis(t *testing.T) {
	for in, want := range map[int64]string{
		0:       "0ms",
		999:     "999ms",
		1500:    "1.5s",
		4604000: "1h16m44s",
	} {
		if got := humanMillis(in); got != want {
			t.Errorf("humanMillis(%d) = %q, want %q", in, got, want)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %s", err)
	}
	prev := os.Stdout
	os.Stdout = w
	prevStdout := stdout
	stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stdout = prev
	stdout = prevStdout
	w.Close()
	out := <-done
	r.Close()
	return out
}
