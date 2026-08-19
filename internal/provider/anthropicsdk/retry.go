package anthropicsdk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/wirelog"
)

// Why this file exists.

var (
	// maxRetryAfter is the longest wait we are willing to sit through inside
	// a turn. Under it, retrying is the right thing: a brief throttle clears
	// and the turn continues.
	maxRetryAfter = 60 * time.Second

	// maxRetries is set explicitly instead of inheriting the SDK's default,
	// so the worst-case silent wait is a property of this file and not of a
	// dependency's release notes.
	maxRetries = 2
)

// rateLimitNote is what the transport saw the last time the provider refused
// us. It rides on the request context so Send can put it in the error after
// the SDK has given up and thrown the response away.
type rateLimitNote struct {
	mu         sync.Mutex
	seen       bool
	status     int
	askedFor   time.Duration // Retry-After exactly as sent
	requestID  string
	resetHint  string // provider's own "when" from its ratelimit headers
	clampCount int
}

func (n *rateLimitNote) record(status int, askedFor time.Duration, requestID, resetHint string, clamped bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = true
	n.status = status
	n.askedFor = askedFor
	n.requestID = requestID
	if resetHint != "" {
		n.resetHint = resetHint
	}
	if clamped {
		n.clampCount++
	}
}

func (n *rateLimitNote) snapshot() (rateLimitNote, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return rateLimitNote{
		seen: n.seen, status: n.status, askedFor: n.askedFor,
		requestID: n.requestID, resetHint: n.resetHint, clampCount: n.clampCount,
	}, n.seen
}

type noteKey struct{}

func withRateLimitNote(ctx context.Context) (context.Context, *rateLimitNote) {
	n := &rateLimitNote{}
	return context.WithValue(ctx, noteKey{}, n), n
}

func noteFromContext(ctx context.Context) *rateLimitNote {
	n, _ := ctx.Value(noteKey{}).(*rateLimitNote)
	return n
}

// retryCapTransport clamps an over-long Retry-After before the SDK's retry
// loop can honor it.
type retryCapTransport struct {
	Inner http.RoundTripper
	Max   time.Duration
	Aria  string
}

func (t *retryCapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	resp, err := inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if !transientStatus(resp.StatusCode) {
		return resp, nil
	}

	asked := wirelog.RetryAfter(resp.Header)
	max := t.Max
	if max <= 0 {
		max = maxRetryAfter
	}
	clamped := asked > max

	requestID := resp.Header.Get("request-id")
	if requestID == "" {
		requestID = resp.Header.Get("x-request-id")
	}
	resetHint := firstResetHint(resp.Header)

	if n := noteFromContext(req.Context()); n != nil {
		n.record(resp.StatusCode, asked, requestID, resetHint, clamped)
	}

	// Say it out loud. Before this, a rate limit produced no log record of
	// any kind: the SDK swallowed it and slept.
	action := "retrying"
	if clamped {
		action = "giving up now (wait exceeds cap; the turn will fail so the aria stays promptable)"
	}
	slog.Warn("anthropic refused a request",
		"aria", t.Aria,
		"status", resp.StatusCode,
		"retry_after", asked.String(),
		"cap", max.String(),
		"action", action,
		"request_id", requestID,
		"reset", resetHint)

	if clamped {
		// x-should-retry is the SDK's own documented override, checked ahead
		// of the status code in requestconfig.shouldRetry. Setting it false
		// makes Execute return this response NOW instead of sleeping: the
		// turn fails, the error carries the wait, and the aria is idle and
		// promptable again within the second.
		resp.Header.Set("x-should-retry", "false")
		resp.Header.Set("Retry-After", formatSeconds(max))
	}
	return resp, nil
}

// transientStatus mirrors what the SDK will retry: rate limit, anthropic's
// overloaded, and 5xx.
func transientStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == 529 || (code >= 500 && code <= 599)
}

func formatSeconds(d time.Duration) string {
	s := int64(d / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

// firstResetHint pulls the provider's own answer to "when will this clear"
// out of its ratelimit headers. Which one is present varies by which limit
// was hit, so take the earliest that names a reset.
func firstResetHint(h http.Header) string {
	best := ""
	for k, vs := range h {
		if len(vs) == 0 {
			continue
		}
		lk := strings.ToLower(k)
		if !strings.Contains(lk, "ratelimit") || !strings.Contains(lk, "reset") {
			continue
		}
		if best == "" || vs[0] < best {
			best = vs[0]
		}
	}
	return best
}

// annotateRateLimit turns a bare "429" into an answer.
func annotateRateLimit(err error, note *rateLimitNote) error {
	if err == nil || note == nil {
		return err
	}
	n, ok := note.snapshot()
	if !ok || n.askedFor <= 0 {
		return err
	}
	msg := fmt.Sprintf("%v: the provider asked us to wait %s", err, n.askedFor.Round(time.Second))
	if n.resetHint != "" {
		msg += fmt.Sprintf(" (limit resets %s)", n.resetHint)
	}
	if n.clampCount > 0 {
		// Say what to DO. A wait this long is a spent quota, and the actions
		// that help are all outside this turn.
		msg += fmt.Sprintf("; that is longer than figaro will hold a turn open (%s), so the turn was ended"+
			" rather than slept through - the aria is free, send again after the reset,"+
			" or fork to a different model", maxRetryAfter)
	}
	if n.requestID != "" {
		msg += fmt.Sprintf(" [%s]", n.requestID)
	}
	return errors.New(msg)
}
