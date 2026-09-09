package wirelog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The whole point of a stream record: a websocket never touches
// http.RoundTripper, so before this the ledger showed nothing at all for a
// provider that streams. One record, with the shape of the stream in it.
func TestStreamRecordsShapeOnFinish(t *testing.T) {
	logs := captureLog(t)

	s := BeginStream(context.Background(), "94f0752b", StreamMethodWS, "wss://api.example.com/v1/stream",
		WithEventVocabulary("response.created", "response.output_item.added",
			"response.function_call_arguments.delta"))
	s.RequestBytes(4096)
	s.Event("response.created", 120)
	s.Event("response.output_item.added", 200)
	s.Event("response.function_call_arguments.delta", 64)
	s.ToolStart()
	s.ArgumentDelta(48, true)
	s.ArgumentDelta(16, false)
	s.Finish("ok", nil)

	got := logs.String()
	if strings.Count(got, RoundLog) != 1 {
		t.Fatalf("want exactly one round-trip record, got:\n%s", got)
	}
	for _, want := range []string{
		`"aria":"94f0752b"`,
		`"method":"WS"`,
		`"url":"wss://api.example.com/v1/stream"`,
		`"req_bytes":4096`,
		`"resp_bytes":384`,
		`"stream_events":3`,
		`"stream_status":"ok"`,
		`"stream_argument_deltas":2`,
		`"stream_argument_bytes":64`,
		`"stream_argument_unmatched":1`,
		`"stream_event_response.created":1`,
		// an unmatched argument delta is a corrupted tool call, not routine
		`"level":"WARN"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("record missing %s:\n%s", want, got)
		}
	}
	// Timings: something must have been recorded for the first event, the
	// first tool-shaped event, and the first argument fragment.
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &rec); err != nil {
		t.Fatalf("record is not one JSON object: %v\n%s", err, got)
	}
	for _, k := range []string{"stream_first_event_ms", "stream_first_tool_ms", "stream_first_argument_ms"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("no %s in the record: %s", k, got)
		}
	}
}

// A clean stream is INFO, so there is something to compare a broken one
// against. Same reason a healthy HTTP round-trip is recorded.
func TestStreamCleanFinishIsInfo(t *testing.T) {
	logs := captureLog(t)
	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	s.Event("response.completed", 32)
	s.Finish("ok", nil)
	if got := logs.String(); !strings.Contains(got, `"level":"INFO"`) {
		t.Errorf("a clean stream should be INFO:\n%s", got)
	}
}

// The rule this package exists under: a websocket error routinely quotes the
// frame it choked on, and that frame carries the model's output and sometimes
// the caller's token. Nothing the caller hands Finish may survive verbatim.
func TestStreamNeverLeaksSecretsOrPayload(t *testing.T) {
	logs := captureLog(t)

	secret := "sk-live-SUPERSECRET-TOKEN"
	payload := "the user's private prose about their divorce"
	err := fmt.Errorf("websocket: bad frame Authorization: Bearer %s body=%q item_id=item_abc123", secret, payload)

	s := BeginStream(context.Background(), "94f0752b", StreamMethodWS,
		"wss://user:hunter2@api.example.com/v1/stream?access_token="+secret+"&session=item_abc123")
	s.RequestBytes(10)
	s.Event("response.output_text.delta "+payload, 4)
	s.Event("mystery.event "+secret, 4)
	s.UnknownEvent()
	s.ArgumentDelta(len(payload), false)
	s.Finish("failed: "+secret, err)

	got := logs.String()
	for _, forbidden := range []string{secret, payload, "hunter2", "item_abc123", "Bearer", "bad frame"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("record leaked %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, `"url":"wss://api.example.com/v1/stream"`) {
		t.Errorf("endpoint should survive stripped of userinfo and query:\n%s", got)
	}
	if !strings.Contains(got, `"stream_err_class"`) {
		t.Errorf("a failure must still be classified:\n%s", got)
	}
	if !strings.Contains(got, `"stream_unknown_events":1`) {
		t.Errorf("unknown event not counted:\n%s", got)
	}
	// The status arrived as "failed: <secret>"; only the recognized first
	// token survives, and a status outside the set collapses to "other".
	if !strings.Contains(got, `"stream_status":"failed"`) {
		t.Errorf("status not reduced to its recognized token:\n%s", got)
	}
	if !strings.Contains(got, `"stream_event_other":2`) {
		t.Errorf("undeclared event names must be bucketed, not retained:\n%s", got)
	}
}

// Event type names come off the wire. Without a declared vocabulary the
// per-type breakdown keeps buckets, never the provider's words.
func TestStreamKeepsOnlyDeclaredEventTypes(t *testing.T) {
	captureLog(t)
	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y",
		WithEventVocabulary("response.created"))
	s.Event("response.created", 1)
	s.Event("response.created", 1)
	s.Event("response.invented_by_the_server", 1)
	s.UnknownEvent()
	defer s.Finish("ok", nil)

	st := s.Stats()
	if st.EventTypes["response.created"] != 2 {
		t.Errorf("declared type not counted under its name: %v", st.EventTypes)
	}
	if st.EventTypes[otherEventType] != 1 {
		t.Errorf("undeclared types not bucketed: %v", st.EventTypes)
	}
	if st.UnknownEvents != 1 {
		t.Errorf("unknown_events = %d", st.UnknownEvents)
	}
	for k := range st.EventTypes {
		if strings.Contains(k, "invented") {
			t.Fatalf("an undeclared event name was retained: %v", st.EventTypes)
		}
	}
}

// The terminal status is metadata: a fixed set, not whatever string a
// provider (or an error formatted into one) hands over.
func TestStreamStatusIsAClosedSet(t *testing.T) {
	for in, want := range map[string]string{
		"ok":                     "ok",
		"incomplete":             "incomplete",
		"cancelled":              "canceled",
		"FAILED":                 "failed",
		"":                       "",
		"whatever the wire said": "other",
	} {
		if got := StreamStatus(in); got != want {
			t.Errorf("StreamStatus(%q) = %q, want %q", in, got, want)
		}
	}

	captureLog(t)
	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	s.Finish("", nil)
	if st := s.Stats(); st.Status != "ok" {
		t.Errorf("an empty status with no error should read ok, got %q", st.Status)
	}
	s2 := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	s2.Finish("", io.ErrUnexpectedEOF)
	if st := s2.Stats(); st.Status != "error" {
		t.Errorf("an empty status with an error should read error, got %q", st.Status)
	}
}

// A stream is the case a log cannot express: it is open for minutes and a
// record only exists once it is over. The running row is the diagnostic.
func TestStreamIsVisibleWhileRunning(t *testing.T) {
	captureLog(t)

	s := BeginStream(context.Background(), "abcd1234", StreamMethodWS, "wss://api.example.com/v1/stream")
	s.RequestBytes(2048)
	s.Event("response.created", 100)

	row, ok := findOutstanding(t, "abcd1234")
	if !ok {
		t.Fatalf("running stream absent from Outstanding: %+v", Outstanding())
	}
	if row.Method != StreamMethodWS || row.ReqBytes != 2048 {
		t.Errorf("row does not describe the stream: %+v", row)
	}
	if row.Stream == nil || row.Stream.Events != 1 || row.Stream.RespBytes != 100 {
		t.Fatalf("live counters missing from the running row: %+v", row.Stream)
	}
	if row.Age() < 0 {
		t.Error("negative age")
	}

	s.Event("response.completed", 20)
	row, _ = findOutstanding(t, "abcd1234")
	if row.Stream.Events != 2 {
		t.Errorf("running counters do not advance: %+v", row.Stream)
	}

	s.Finish("ok", nil)
	if _, ok := findOutstanding(t, "abcd1234"); ok {
		t.Fatalf("stream stayed outstanding after Finish: %+v", Outstanding())
	}
}

// Providers defer Finish and also finish explicitly on the error path. Two
// records for one attempt would double-count every stream in the ledger.
func TestStreamFinishIsIdempotentAndClosesTheRecord(t *testing.T) {
	logs := captureLog(t)

	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	s.Event("a", 10)
	s.Finish("failed", io.ErrUnexpectedEOF)
	s.Finish("ok", nil)
	// Late stragglers from a cancelled read loop must not mutate a closed
	// record, and must not resurrect the in-flight row.
	s.Event("b", 10)
	s.UnknownEvent()
	s.ToolStart()
	s.ArgumentDelta(10, false)
	s.RequestBytes(10)
	s.Finish("ok", nil)

	got := logs.String()
	if n := strings.Count(got, RoundLog); n != 1 {
		t.Fatalf("Finish wrote %d records, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, `"stream_events":1`) || strings.Contains(got, `"stream_argument_deltas"`) {
		t.Errorf("a finished stream kept accumulating:\n%s", got)
	}
	if !strings.Contains(got, `"stream_err_class":"unexpected_eof"`) {
		t.Errorf("first Finish should own the outcome:\n%s", got)
	}
	if len(Outstanding()) != 0 {
		t.Errorf("in-flight entry leaked: %+v", Outstanding())
	}
	st := s.Stats()
	if st.Events != 1 {
		t.Errorf("Stats after Finish should be the final state: %+v", st)
	}
	if st.Status != "failed" || st.ErrClass != "unexpected_eof" {
		t.Errorf("Stats after Finish lost the terminal outcome: %+v", st)
	}
}

// The read loop and the finisher are different goroutines; nothing here may
// race or lose a count. Run under -race.
func TestStreamConcurrentEventsAreCountedExactly(t *testing.T) {
	captureLog(t)

	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Event("response.output_text.delta", 3)
				s.ArgumentDelta(2, j%2 == 0)
			}
		}()
	}
	// Outstanding snapshots the same stream while it is being written.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				Outstanding()
			}
		}
	}()
	wg.Wait()
	close(stop)

	st := s.Stats()
	if st.Events != 800 || st.RespBytes != 2400 {
		t.Errorf("lost events: %+v", st)
	}
	if st.ArgumentDeltas != 800 || st.ArgumentBytes != 1600 || st.ArgumentUnmatched != 400 {
		t.Errorf("lost argument deltas: %+v", st)
	}
	s.Finish("ok", nil)
}

// Two goroutines racing to Finish must still produce one record.
func TestStreamConcurrentFinishWritesOneRecord(t *testing.T) {
	logs := captureLog(t)
	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Finish("ok", nil) }()
	}
	wg.Wait()
	if n := strings.Count(logs.String(), RoundLog); n != 1 {
		t.Fatalf("%d records from a contested Finish", n)
	}
}

// A provider that renames its events every frame must not grow the process's
// memory. Cardinality is bounded; the overflow is counted, not kept.
func TestStreamEventTypeCardinalityIsBounded(t *testing.T) {
	captureLog(t)
	s := BeginStream(context.Background(), "aaaa1111", StreamMethodWS, "wss://x/y")
	vocab := make([]string, 0, maxEventTypes*10)
	for i := 0; i < maxEventTypes*10; i++ {
		vocab = append(vocab, fmt.Sprintf("event.%d", i))
	}
	WithEventVocabulary(vocab...)(s)
	for _, name := range vocab {
		s.Event(name, 1)
	}
	st := s.Stats()
	if len(st.EventTypes) != maxEventTypes {
		t.Errorf("kept %d distinct types, want the cap %d", len(st.EventTypes), maxEventTypes)
	}
	if st.TypesDropped == 0 {
		t.Error("overflow was not counted")
	}
	if st.Events != int64(maxEventTypes*10) {
		t.Errorf("events lost to the cap: %d", st.Events)
	}
	s.Finish("ok", nil)
}

type codedError struct{ code string }

func (e codedError) Error() string          { return "secret payload: sk-live-XYZ" }
func (e codedError) DiagnosticCode() string { return e.code }

func TestErrorClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", context.Canceled, "canceled"},
		{"wrapped canceled", fmt.Errorf("read: %w", context.Canceled), "canceled"},
		{"deadline", context.DeadlineExceeded, "deadline_exceeded"},
		{"unexpected eof", io.ErrUnexpectedEOF, "unexpected_eof"},
		{"eof", io.EOF, "eof"},
		{"closed", net.ErrClosed, "closed"},
		{"provider code", codedError{"ws_protocol_error"}, "ws_protocol_error"},
		// A code is one word: anything past the first non-identifier rune is
		// dropped rather than transliterated, so a message passed as a code
		// cannot survive in pieces.
		{"provider code sanitized", codedError{"bad_frame: Bearer sk-live-XYZ"}, "bad_frame"},
		{"provider code empty falls through", codedError{""}, "wirelog.codederror"},
	} {
		if got := ErrorClass(tc.err); got != tc.want {
			t.Errorf("%s: ErrorClass = %q, want %q", tc.name, got, tc.want)
		}
	}

	// The default class is a Go type name, never the message.
	err := errors.New("Authorization: Bearer sk-live-XYZ")
	if got := ErrorClass(err); strings.Contains(got, "sk-live") || strings.Contains(got, "bearer") {
		t.Fatalf("error text survived classification: %q", got)
	}
	// net.Error timeouts are their own diagnosis: a stalled socket is not a
	// refused one.
	if got := ErrorClass(&net.DNSError{IsTimeout: true}); got != "net_timeout" {
		t.Errorf("timeout class = %q", got)
	}
}

func TestSanitizeEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"wss://user:pw@host/v1/stream?token=abc#frag": "wss://host/v1/stream",
		"wss://host:8443/v1/stream":                   "wss://host:8443/v1/stream",
		"":                                            "",
		// Anything that is not scheme://host/path is refused whole: a
		// textual strip is where a credential survives.
		"host/v1?x=1":            InvalidEndpoint,
		"mailto:user:pw@host":    InvalidEndpoint,
		"://token@host/x":        InvalidEndpoint,
		"wss://host/x\x7f?t=abc": InvalidEndpoint,
	} {
		if got := SanitizeEndpoint(in); got != want {
			t.Errorf("SanitizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

// A nil Stream is what a provider holds when diagnostics are off. Every
// method has to tolerate it, or the observer becomes a crash site.
func TestNilStreamIsInert(t *testing.T) {
	var s *Stream
	s.RequestBytes(1)
	s.Event("x", 1)
	s.UnknownEvent()
	s.ToolStart()
	s.ArgumentDelta(1, false)
	s.Finish("ok", nil)
	if st := s.Stats(); st.Events != 0 {
		t.Errorf("nil stream produced stats: %+v", st)
	}
}

// BeginStream falls back to the aria already on the context, so a caller that
// forgot to pass one still gets an attributed row.
func TestBeginStreamTakesAriaFromContext(t *testing.T) {
	captureLog(t)
	s := BeginStream(WithAria(context.Background(), "ctxaria1"), "", "", "wss://x/y")
	defer s.Finish("ok", nil)
	if _, ok := findOutstanding(t, "ctxaria1"); !ok {
		t.Fatalf("aria not taken from context: %+v", Outstanding())
	}
	if s.method != StreamMethodWS {
		t.Errorf("method default = %q", s.method)
	}
}

func findOutstanding(t *testing.T, aria string) (InFlight, bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		for _, f := range Outstanding() {
			if f.Aria == aria {
				return f, true
			}
		}
		if time.Now().After(deadline) {
			return InFlight{}, false
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStreamDefaultEventPathAllocatesNothing(t *testing.T) {
	s := BeginStream(context.Background(), "allocation-probe", "WS", "wss://example.com/responses")
	defer s.Finish("completed", nil)
	allocations := testing.AllocsPerRun(100, func() {
		s.Event("response.function_call_arguments.delta", 128)
		s.ArgumentDelta(12, true)
	})
	if allocations != 0 {
		t.Fatalf("default stream accounting allocated %.1f objects per event", allocations)
	}
}
