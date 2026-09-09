package wirelog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Streaming transports (websocket today) never touch http.RoundTripper, so
// the ledger could not see them at all: an aria talking over a socket looked
// idle. A Stream is the same diagnostic contract as a round-trip, driven by
// the provider instead of the transport, and it is transport-independent on
// purpose - nothing here knows what a websocket is.
//
// What it deliberately does NOT keep: raw error text, headers, bodies, event
// payloads, content, or item/tool identifiers. A stream carries the model's
// output and the caller's credentials; the only safe things to record are
// counts, sizes, timings, schema-level event type names, and a class.

const (
	// StreamMethodWS is the method a websocket stream is filed under, so the
	// ledger's method column keeps meaning something.
	StreamMethodWS = "WS"

	maxEventTypes  = 24 // distinct type names retained per stream
	maxTokenLen    = 48
	maxErrClassLen = 64

	// otherEventType is where an event whose name is not in the caller's
	// declared vocabulary is counted. The name itself is discarded: it came
	// off the wire, and anything off the wire is payload until proven
	// otherwise.
	otherEventType = "other"
)

// streamStatuses is the closed set of terminal statuses. A provider passes one
// of these or the stream is filed under "other": a status is metadata, and
// metadata does not come off the wire verbatim.
var streamStatuses = map[string]bool{
	"ok": true, "completed": true, "incomplete": true, "failed": true,
	"error": true, "canceled": true, "cancelled": true, "interrupted": true,
	"timeout": true, "refused": true, "closed": true, "unknown": true,
}

// StreamStatus reduces a caller's status to the recognized set.
func StreamStatus(status string) string {
	tok := sanitizeToken(status, maxTokenLen)
	if tok == "" {
		return ""
	}
	if streamStatuses[tok] {
		if tok == "cancelled" {
			return "canceled"
		}
		return tok
	}
	return "other"
}

// StreamOption configures a stream at Begin.
type StreamOption func(*Stream)

// WithEventVocabulary declares the event type names the caller's parser
// recognizes. Only these are retained per-type; everything else is counted
// under "other" with its name discarded. Without it a stream keeps totals
// only, which is the safe default: wirelog cannot know a provider's schema,
// and an event type read off a frame is not metadata until someone says it is.
func WithEventVocabulary(names ...string) StreamOption {
	return func(s *Stream) {
		if s.vocab == nil {
			s.vocab = make(map[string]bool, len(names))
		}
		for _, n := range names {
			if tok := sanitizeToken(n, maxTokenLen); tok != "" {
				s.vocab[tok] = true
			}
		}
	}
}

// StreamStats is a snapshot of one stream's accounting. Safe to serialize:
// every field is a count, a size, a duration, or a bounded token.
type StreamStats struct {
	Status   string `json:"status,omitempty"`
	ErrClass string `json:"err_class,omitempty"`

	Events        int64 `json:"events"`
	UnknownEvents int64 `json:"unknown_events,omitempty"`
	RespBytes     int64 `json:"resp_bytes,omitempty"`

	FirstEventMS    int64 `json:"first_event_ms,omitempty"`
	FirstToolMS     int64 `json:"first_tool_ms,omitempty"`
	FirstArgumentMS int64 `json:"first_argument_ms,omitempty"`

	ArgumentDeltas    int64 `json:"argument_deltas,omitempty"`
	ArgumentBytes     int64 `json:"argument_bytes,omitempty"`
	ArgumentUnmatched int64 `json:"argument_unmatched,omitempty"`

	// EventTypes is bounded at maxEventTypes distinct names; anything past
	// that is counted in TypesDropped rather than growing the map.
	EventTypes   map[string]int64 `json:"event_types,omitempty"`
	TypesDropped int64            `json:"types_dropped,omitempty"`
}

// Stream is one streaming provider attempt: visible in Outstanding while it
// runs, one RoundLog record when it finishes. Every method is safe on a nil
// receiver and after Finish, so a provider can defer Finish and keep calling
// without guarding.
type Stream struct {
	ctx      context.Context
	seq      uint64
	aria     string
	method   string
	endpoint string
	started  time.Time

	mu        sync.Mutex
	done      bool
	status    string
	errClass  string
	reqBytes  int64
	respBytes int64

	events  int64
	unknown int64

	firstEvent time.Duration
	firstTool  time.Duration
	firstArg   time.Duration

	argDeltas    int64
	argBytes     int64
	argUnmatched int64

	types        map[string]int64
	typesDropped int64

	vocab map[string]bool
}

// BeginStream opens a stream record. endpoint is stripped of userinfo and
// query before it is retained: those are where credentials live.
func BeginStream(ctx context.Context, aria, method, endpoint string, opts ...StreamOption) *Stream {
	if ctx == nil {
		ctx = context.Background()
	}
	if method == "" {
		method = StreamMethodWS
	}
	if aria == "" {
		if c, ok := cfgFromContext(ctx); ok {
			aria = c.aria
		}
	}
	s := &Stream{
		ctx:      ctx,
		aria:     aria,
		method:   method,
		endpoint: SanitizeEndpoint(endpoint),
		started:  time.Now(),
		types:    make(map[string]int64, 8),
	}
	for _, opt := range opts {
		opt(s)
	}
	departStream(s)
	return s
}

// RequestBytes records the size of what was sent to open the stream. Additive:
// a provider that sends more mid-stream can call it again.
func (s *Stream) RequestBytes(n int64) {
	if s == nil || n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.reqBytes += n
}

// Event records one decoded event. eventType is a schema-level name
// ("response.output_item.added"), never content. receivedBytes is the size of
// the frame it was decoded from. Call it for EVERY decoded event.
func (s *Stream) Event(eventType string, receivedBytes int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	since := time.Since(s.started)
	s.events++
	if receivedBytes > 0 {
		s.respBytes += int64(receivedBytes)
	}
	if s.firstEvent == 0 {
		s.firstEvent = since
	}
	name := s.bucket(sanitizeToken(eventType, maxTokenLen))
	if _, ok := s.types[name]; !ok && len(s.types) >= maxEventTypes {
		s.typesDropped++
		return
	}
	s.types[name]++
}

// UnknownEvent marks the event just recorded as one the parser did not
// recognize: call it from the parser's default branch, after Event. A rising
// count is the early warning that the provider's schema moved.
func (s *Stream) UnknownEvent() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.unknown++
}

// ToolStart marks the first evidence of a tool call. Only the parser knows
// which events those are - an output item can be anything - so the timing is
// declared, not guessed.
func (s *Stream) ToolStart() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.firstTool != 0 {
		return
	}
	s.firstTool = time.Since(s.started)
}

// ArgumentDelta records a tool-argument fragment. bytes is the fragment's
// size, never its text. matched=false means the delta named a call the
// accumulator has no record of - the shape of a silent tool-call corruption,
// and the reason this method exists at all.
func (s *Stream) ArgumentDelta(bytes int, matched bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	if s.firstArg == 0 {
		s.firstArg = time.Since(s.started)
	}
	s.argDeltas++
	if bytes > 0 {
		s.argBytes += int64(bytes)
	}
	if !matched {
		s.argUnmatched++
	}
}

// Finish completes the stream: one RoundLog record, one span event, and the
// in-flight row goes away. Idempotent - later calls are ignored, so a defer
// plus an explicit call on the error path is fine.
//
// err is classified, never quoted: websocket errors routinely carry the frame
// that failed, which carries the payload and sometimes the token.
func (s *Stream) Finish(status string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.status = StreamStatus(status)
	s.errClass = ErrorClass(err)
	if s.status == "" {
		if s.errClass == "" {
			s.status = "ok"
		} else {
			s.status = "error"
		}
	}
	stats := s.snapshotLocked()
	ctx, aria, method, endpoint := s.ctx, s.aria, s.method, s.endpoint
	reqBytes, started, seq := s.reqBytes, s.started, s.seq
	s.mu.Unlock()

	arriveStream(seq)

	d := time.Since(started)
	logStream(ctx, aria, method, endpoint, d, reqBytes, stats)
	emitStreamMeta(ctx, aria, method, endpoint, d, reqBytes, stats)
}

// Stats reports the stream's accounting so far, running or finished.
func (s *Stream) Stats() StreamStats {
	if s == nil {
		return StreamStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// row is the in-flight ledger view of a running stream.
func (s *Stream) row() InFlight {
	s.mu.Lock()
	st := s.snapshotLocked()
	reqBytes := s.reqBytes
	s.mu.Unlock()
	return InFlight{
		Seq: s.seq, Aria: s.aria, Method: s.method, URL: s.endpoint,
		StartedAt: s.started, ReqBytes: reqBytes, Stream: &st,
	}
}

func (s *Stream) snapshotLocked() StreamStats {
	st := StreamStats{
		Status:            s.status,
		ErrClass:          s.errClass,
		Events:            s.events,
		UnknownEvents:     s.unknown,
		RespBytes:         s.respBytes,
		FirstEventMS:      millis(s.firstEvent),
		FirstToolMS:       millis(s.firstTool),
		FirstArgumentMS:   millis(s.firstArg),
		ArgumentDeltas:    s.argDeltas,
		ArgumentBytes:     s.argBytes,
		ArgumentUnmatched: s.argUnmatched,
		TypesDropped:      s.typesDropped,
	}
	if len(s.types) > 0 {
		st.EventTypes = make(map[string]int64, len(s.types))
		for k, v := range s.types {
			st.EventTypes[k] = v
		}
	}
	return st
}

func millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 1 // a sub-millisecond first event happened; 0 reads as "never"
}

// StreamAttrPrefix is the flat-attr namespace the ledger reads back. The
// names below are the whole contract between wirelog and the daemon.
const StreamAttrPrefix = "stream_"

// StreamEventTypeAttrPrefix namespaces the per-type counters, the way
// ratelimit_ namespaces harvested headers.
const StreamEventTypeAttrPrefix = "stream_event_"

func logStream(ctx context.Context, aria, method, endpoint string, d time.Duration, reqBytes int64, st StreamStats) {
	level := slog.LevelInfo
	if st.ErrClass != "" || (st.Status != "ok" && st.Status != "completed") {
		level = slog.LevelWarn
	} else if st.ArgumentUnmatched > 0 || st.UnknownEvents > 0 {
		level = slog.LevelWarn
	}

	attrs := []any{
		"aria", aria,
		"method", method,
		"url", endpoint,
		"duration_ms", d.Milliseconds(),
		"req_bytes", reqBytes,
		"stream_status", st.Status,
		"stream_events", st.Events,
		"resp_bytes", st.RespBytes,
	}
	if st.ErrClass != "" {
		attrs = append(attrs, "stream_err_class", st.ErrClass)
	}
	if st.UnknownEvents > 0 {
		attrs = append(attrs, "stream_unknown_events", st.UnknownEvents)
	}
	if st.FirstEventMS > 0 {
		attrs = append(attrs, "stream_first_event_ms", st.FirstEventMS)
	}
	if st.FirstToolMS > 0 {
		attrs = append(attrs, "stream_first_tool_ms", st.FirstToolMS)
	}
	if st.FirstArgumentMS > 0 {
		attrs = append(attrs, "stream_first_argument_ms", st.FirstArgumentMS)
	}
	if st.ArgumentDeltas > 0 {
		attrs = append(attrs, "stream_argument_deltas", st.ArgumentDeltas,
			"stream_argument_bytes", st.ArgumentBytes)
	}
	if st.ArgumentUnmatched > 0 {
		attrs = append(attrs, "stream_argument_unmatched", st.ArgumentUnmatched)
	}
	if st.TypesDropped > 0 {
		attrs = append(attrs, "stream_types_dropped", st.TypesDropped)
	}
	for name, n := range st.EventTypes {
		attrs = append(attrs, StreamEventTypeAttrPrefix+name, n)
	}
	slog.Default().Log(ctx, level, RoundLog, attrs...)
}

func emitStreamMeta(ctx context.Context, aria, method, endpoint string, d time.Duration, reqBytes int64, st StreamStats) {
	attrs := []attribute.KeyValue{
		attribute.String("http.method", method),
		attribute.String("http.url", endpoint),
		attribute.Int64("http.duration_ms", d.Milliseconds()),
		attribute.Int64("http.req_bytes", reqBytes),
		attribute.Int64("http.resp_bytes", st.RespBytes),
		attribute.String("stream.status", st.Status),
		attribute.Int64("stream.events", st.Events),
	}
	if aria != "" {
		attrs = append(attrs, attribute.String("figaro.aria", aria))
	}
	if st.ErrClass != "" {
		attrs = append(attrs, attribute.String("stream.err_class", st.ErrClass))
	}
	if st.UnknownEvents > 0 {
		attrs = append(attrs, attribute.Int64("stream.unknown_events", st.UnknownEvents))
	}
	if st.FirstEventMS > 0 {
		attrs = append(attrs, attribute.Int64("stream.first_event_ms", st.FirstEventMS))
	}
	if st.FirstToolMS > 0 {
		attrs = append(attrs, attribute.Int64("stream.first_tool_ms", st.FirstToolMS))
	}
	if st.FirstArgumentMS > 0 {
		attrs = append(attrs, attribute.Int64("stream.first_argument_ms", st.FirstArgumentMS))
	}
	if st.ArgumentDeltas > 0 {
		attrs = append(attrs,
			attribute.Int64("stream.argument_deltas", st.ArgumentDeltas),
			attribute.Int64("stream.argument_bytes", st.ArgumentBytes))
	}
	if st.ArgumentUnmatched > 0 {
		attrs = append(attrs, attribute.Int64("stream.argument_unmatched", st.ArgumentUnmatched))
	}
	figOtel.Event(ctx, "provider.stream", attrs...)
}

// diagnosticCoder lets a provider name its own failure without handing over
// the error text. Anything it returns is still sanitized and truncated.
type diagnosticCoder interface{ DiagnosticCode() string }

// ErrorClass names a failure without quoting it. A websocket error commonly
// embeds the frame it choked on, so err.Error() is never safe to retain.
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	var dc diagnosticCoder
	if errors.As(err, &dc) {
		if code := sanitizeToken(dc.DiagnosticCode(), maxErrClassLen); code != "" {
			return code
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	case errors.Is(err, io.ErrClosedPipe):
		return "closed"
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return "net_timeout"
		}
		return "net"
	}
	// A Go type name carries no payload; the message does. Keep the type.
	return sanitizeToken(strings.ToLower(typeName(err)), maxErrClassLen)
}

func typeName(err error) string {
	name := fmt.Sprintf("%T", err)
	name = strings.TrimPrefix(name, "*")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// sanitizeToken reduces a caller-supplied label to a bounded identifier: the
// leading run of identifier characters, truncated. Stopping at the first
// disallowed rune is the load-bearing part - it is what turns an error string
// that someone passed as a status ("failed: Bearer sk-live-...") into one
// harmless word instead of a redacted-looking copy of the secret.
func sanitizeToken(s string, max int) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= max {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			return strings.Trim(b.String(), "_-.")
		}
	}
	return strings.Trim(b.String(), "_-.")
}

// bucket names the counter an event is filed under. Only a declared
// vocabulary term survives as itself.
func (s *Stream) bucket(name string) string {
	if name != "" && s.vocab[name] {
		return name
	}
	return otherEventType
}

// InvalidEndpoint stands in for anything that does not parse as a plain
// scheme://host/path URL.
const InvalidEndpoint = "<invalid-endpoint>"

// SanitizeEndpoint keeps scheme, host and path and drops everything a
// credential can hide in: userinfo, query, fragment. Anything it cannot parse
// into exactly that shape is refused outright rather than trimmed by hand: a
// malformed or opaque URL is where a token survives a textual strip.
func SanitizeEndpoint(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Scheme == "" || u.Host == "" {
		return InvalidEndpoint
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}
