package wirelog

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RoundLog is the slog message the daemon's log ring retains regardless of
// level.
const RoundLog = "provider round-trip"

type InFlight struct {
	Seq       uint64       `json:"seq"`
	Aria      string       `json:"aria,omitempty"`
	Method    string       `json:"method"`
	URL       string       `json:"url"`
	StartedAt time.Time    `json:"started_at"`
	ReqBytes  int64        `json:"req_bytes,omitempty"`
	Stream    *StreamStats `json:"stream,omitempty"`
}

func (f InFlight) Age() time.Duration { return time.Since(f.StartedAt) }

var inflight = struct {
	mu      sync.Mutex
	m       map[uint64]InFlight
	streams map[uint64]*Stream
	seq     uint64
}{m: map[uint64]InFlight{}, streams: map[uint64]*Stream{}}

func depart(aria, method, url string, reqBytes int64) uint64 {
	inflight.mu.Lock()
	defer inflight.mu.Unlock()
	inflight.seq++
	inflight.m[inflight.seq] = InFlight{
		Seq: inflight.seq, Aria: aria, Method: method, URL: url,
		StartedAt: time.Now(), ReqBytes: reqBytes,
	}
	return inflight.seq
}

func arrive(seq uint64) (InFlight, bool) {
	inflight.mu.Lock()
	defer inflight.mu.Unlock()
	f, ok := inflight.m[seq]
	delete(inflight.m, seq)
	return f, ok
}

// departStream stamps the seq and publishes under one lock, or a reader sees
// a zero seq.
func departStream(s *Stream) {
	inflight.mu.Lock()
	defer inflight.mu.Unlock()
	inflight.seq++
	s.seq = inflight.seq
	inflight.streams[s.seq] = s
}

func arriveStream(seq uint64) {
	inflight.mu.Lock()
	defer inflight.mu.Unlock()
	delete(inflight.streams, seq)
}

// Outstanding lists requests that have not returned, oldest first.
func Outstanding() []InFlight {
	inflight.mu.Lock()
	out := make([]InFlight, 0, len(inflight.m)+len(inflight.streams))
	for _, f := range inflight.m {
		out = append(out, f)
	}
	live := make([]*Stream, 0, len(inflight.streams))
	for _, s := range inflight.streams {
		live = append(live, s)
	}
	inflight.mu.Unlock()

	// Outside the registry lock: Finish takes the stream mutex first.
	for _, s := range live {
		out = append(out, s.row())
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.Before(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func logRound(ctx context.Context, f InFlight, resp *http.Response, d time.Duration, err error) {
	level := slog.LevelInfo
	attrs := []any{
		"aria", f.Aria,
		"method", f.Method,
		"url", f.URL,
		"duration_ms", d.Milliseconds(),
		"req_bytes", f.ReqBytes,
	}
	switch {
	case err != nil:
		level = slog.LevelWarn
		attrs = append(attrs, "err", err.Error())
	case resp != nil:
		attrs = append(attrs, "status", resp.StatusCode)
		if rid := requestID(resp.Header); rid != "" {
			attrs = append(attrs, "request_id", rid)
		}
		if ra := RetryAfter(resp.Header); ra > 0 {
			attrs = append(attrs, "retry_after_s", int64(ra/time.Second))
		}
		for k, v := range rateLimitHeaders(resp.Header) {
			attrs = append(attrs, "ratelimit_"+strings.ReplaceAll(k, "-", "_"), v)
		}
		if resp.StatusCode >= 400 {
			level = slog.LevelWarn
		}
	}
	slog.Default().Log(ctx, level, RoundLog, attrs...)
}

// RetryAfter reads only the seconds form; an HTTP-date reads as absent.
func RetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func rateLimitHeaders(h http.Header) map[string]string {
	var out map[string]string
	for k, vs := range h {
		lk := strings.ToLower(k)
		if !strings.Contains(lk, "ratelimit") || len(vs) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]string, 4)
		}
		key := lk
		for _, pfx := range []string{"anthropic-ratelimit-", "x-ratelimit-", "ratelimit-"} {
			if strings.HasPrefix(lk, pfx) {
				key = strings.TrimPrefix(lk, pfx)
				break
			}
		}
		out[key] = vs[0]
	}
	return out
}

func requestID(h http.Header) string {
	if v := h.Get("request-id"); v != "" {
		return v
	}
	return h.Get("x-request-id")
}
