package anthropic

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/provider"
)

// gluck, 0.57.1: every attempt of three turns got a 429 in under a second,
// anthropic-ratelimit-unified-status rejected and Retry-After about 37 h. The
// retry slept toward it inside the first-byte window, the window fired, and
// the turn reported "no byte arrived within 5m0s" after three resumes.

func TestARejectedRateLimitFailsAtOnceWithItsReset(t *testing.T) {
	reset := time.Now().Add(37 * time.Hour).Truncate(time.Second)
	srv, hits := scriptedGateway(t, func(_ int32, h http.Header) int {
		h.Set("Retry-After", "132670")
		h.Set("anthropic-ratelimit-unified-status", "rejected")
		h.Set("anthropic-ratelimit-unified-representative-claim", "seven_day")
		h.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset.Unix(), 10))
		return http.StatusTooManyRequests
	})
	start := time.Now()
	err := sendThroughTheClock(t, srv, 5*time.Second, nil)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a rejected rate limit took %s to fail", took)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Fatalf("a rejected rate limit was sent %d times", n)
	}
	if _, cut := provider.AsStreamCut(err); cut {
		t.Fatalf("a rejected rate limit reads as a stream cut, which the turn resumes: %v", err)
	}
	var limit *provider.RateLimited
	if !errors.As(err, &limit) {
		t.Fatalf("got %v, want a RateLimited", err)
	}
	want := "anthropic: 7-day rate limit reached; resets " + reset.Local().Format("Mon 2 Jan 15:04 MST")
	if err.Error() != want {
		t.Fatalf("the reason reads %q, want %q", err.Error(), want)
	}
}

func TestARateLimitLongerThanTheBoundFailsAtOnce(t *testing.T) {
	srv, hits := scriptedGateway(t, func(_ int32, h http.Header) int {
		h.Set("Retry-After", "600")
		return http.StatusTooManyRequests
	})
	start := time.Now()
	err := sendThroughTheClock(t, srv, 5*time.Second, nil)
	if took := time.Since(start); took > time.Second || atomic.LoadInt32(hits) != 1 {
		t.Fatalf("a 10-minute Retry-After was waited on: %s, %d sends", took, atomic.LoadInt32(hits))
	}
	if !strings.HasPrefix(err.Error(), "anthropic: rate limit reached; resets ") {
		t.Fatalf("the reason reads %q", err)
	}
}
