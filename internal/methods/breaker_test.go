package methods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) pass(d time.Duration) { c.t = c.t.Add(d) }
func testBreaker() (*Breaker, *clock) {
	c := &clock{t: time.Date(2026, 11, 8, 0, 30, 0, 0, time.UTC)}
	b := NewBreaker()
	b.now = c.now
	return b, c
}

type routeAsker struct {
	calls atomic.Int32
	err   atomic.Value
}

func (a *routeAsker) fail(err error) { a.err.Store(&err) }
func (a *routeAsker) heal()          { var none error; a.err.Store(&none) }

func (a *routeAsker) Ask(ctx context.Context, _ decide.Source, q decide.Question) (rpc.DecideResponse, error) {
	a.calls.Add(1)
	if p, _ := a.err.Load().(*error); p != nil && *p != nil {
		return rpc.DecideResponse{}, *p
	}
	return rpc.DecideResponse{Rows: []rpc.DecideRow{{Option: q.Options[0].Label, Chosen: true}}}, nil
}

func routed(provider, model string) decide.Source {
	set := map[string]json.RawMessage{
		decide.ProviderKey: json.RawMessage(`"` + provider + `"`),
		decide.ModelKey:    json.RawMessage(`"` + model + `"`),
	}
	return decide.Source{Defaults: form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))}
}

var quota = fmt.Errorf("%w: decide: HTTP 429: no credits", decide.ErrUpstream)

func moodMethod(t *testing.T) Method {
	t.Helper()
	ms, errs := Read(bound(t, `{"mood": {"key": "mood", "context": "{{ inquiry }}", "options": ["calm", "angry"], "default": "calm"}}`))
	if len(errs) != 0 || len(ms) != 1 {
		t.Fatalf("methods %+v, errs %v", ms, errs)
	}
	return ms[0]
}

func runs(t *testing.T, a Asker, src decide.Source, n int) []Outcome {
	t.Helper()
	m := moodMethod(t)
	out := make([]Outcome, n)
	for i := range out {
		out[i] = Run(context.Background(), a, src, m, []string{"hello"})
	}
	return out
}

func TestARouteThatKeepsFailingIsNotAskedAgainUntilItsRestEnds(t *testing.T) {
	b, c := testBreaker()
	up := &routeAsker{}
	up.fail(quota)
	src := routed("rou2", "gpt-6-luna")

	for i, o := range runs(t, b.Guard(up), src, 10) {
		if o.Verdict != Defaulted || o.Value != "calm" {
			t.Fatalf("run %d: %+v, want the default written", i, o)
		}
	}
	if got := up.calls.Load(); got != RestAfter {
		t.Fatalf("the failing route was asked %d times in 10 runs, want %d", got, RestAfter)
	}

	c.pass(FirstRest - time.Second)
	runs(t, b.Guard(up), src, 1)
	if got := up.calls.Load(); got != RestAfter {
		t.Fatalf("asked during its rest: %d calls", got)
	}

	c.pass(time.Second)
	runs(t, b.Guard(up), src, 3)
	if got := up.calls.Load(); got != RestAfter+1 {
		t.Fatalf("after the rest, %d calls; want exactly one probe", got)
	}
	rest := b.Resting()
	if len(rest) != 1 || rest[0].Failures != RestAfter+1 || time.UnixMilli(rest[0].NextTryMS).Sub(c.t) != 2*FirstRest {
		t.Fatalf("a failed probe doubles the rest: %+v", rest)
	}
}

func TestAnAnswerEndsTheRest(t *testing.T) {
	b, c := testBreaker()
	up := &routeAsker{}
	up.fail(quota)
	src := routed("rou2", "gpt-6-luna")
	runs(t, b.Guard(up), src, RestAfter)
	if len(b.Resting()) != 1 {
		t.Fatalf("resting %+v", b.Resting())
	}

	up.heal()
	c.pass(FirstRest)
	runs(t, b.Guard(up), src, 1)
	if len(b.Resting()) != 0 {
		t.Fatalf("still resting after an answer: %+v", b.Resting())
	}
	before := up.calls.Load()
	if o := runs(t, b.Guard(up), src, 2); o[1].Verdict != Decided {
		t.Fatalf("after the answer: %+v", o[1])
	}
	if up.calls.Load() != before+2 {
		t.Fatal("a healed route is not asked every time")
	}
}

func TestTheRestNeverPassesHalfAnHour(t *testing.T) {
	b, c := testBreaker()
	up := &routeAsker{}
	up.fail(quota)
	src := routed("rou2", "gpt-6-luna")
	runs(t, b.Guard(up), src, RestAfter)
	for range 12 {
		c.pass(LongRest)
		runs(t, b.Guard(up), src, 1)
	}
	rest := b.Resting()
	if len(rest) != 1 || time.UnixMilli(rest[0].NextTryMS).Sub(c.t) != LongRest {
		t.Fatalf("rest %+v", rest)
	}
}

func TestOnlyFailuresInARowCount(t *testing.T) {
	b, _ := testBreaker()
	up := &routeAsker{}
	src := routed("rou2", "gpt-6-luna")
	for range 5 {
		up.fail(quota)
		runs(t, b.Guard(up), src, RestAfter-1)
		up.heal()
		runs(t, b.Guard(up), src, 1)
	}
	if r := b.Resting(); len(r) != 0 {
		t.Fatalf("an answer between failures must reset the count: %+v", r)
	}
}

func TestOneRouteRestingLeavesAnotherAlone(t *testing.T) {
	b, _ := testBreaker()
	up := &routeAsker{}
	up.fail(quota)
	runs(t, b.Guard(up), routed("rou2", "gpt-6-luna"), RestAfter)
	before := up.calls.Load()
	runs(t, b.Guard(up), routed("experiential", "jev-latest"), 1)
	if up.calls.Load() != before+1 {
		t.Fatal("a different route was skipped")
	}
}

func TestOnlyTheRouteItselfFailingCounts(t *testing.T) {
	src := routed("rou2", "gpt-6-luna")
	for name, err := range map[string]error{
		"a refusal before any request": errors.New(`sandbox "chat" does not allow provider "rou2"`),
		"the caller giving up":         fmt.Errorf("%w: %w", decide.ErrUpstream, context.Canceled),
	} {
		b, _ := testBreaker()
		up := &routeAsker{}
		up.fail(err)
		runs(t, b.Guard(up), src, 2*RestAfter)
		if got := up.calls.Load(); got != 2*RestAfter {
			t.Errorf("%s: asked %d of %d times", name, got, 2*RestAfter)
		}
	}
	b, _ := testBreaker()
	up := &routeAsker{}
	up.fail(context.DeadlineExceeded)
	runs(t, b.Guard(up), src, 2*RestAfter)
	if got := up.calls.Load(); got != RestAfter {
		t.Errorf("a route that never answers in time was asked %d times", got)
	}
}

func TestOneProbeAtATime(t *testing.T) {
	b, c := testBreaker()
	up := &routeAsker{}
	up.fail(quota)
	src := routed("rou2", "gpt-6-luna")
	runs(t, b.Guard(up), src, RestAfter)
	c.pass(FirstRest)

	entered, release := make(chan struct{}), make(chan struct{})
	slow := askerFunc(func(ctx context.Context, s decide.Source, q decide.Question) (rpc.DecideResponse, error) {
		close(entered)
		<-release
		return up.Ask(ctx, s, q)
	})
	g := b.Guard(slow)
	q := moodMethod(t).Question([]string{"hi"})
	probed := make(chan error, 1)
	go func() {
		_, err := g.Ask(context.Background(), src, q)
		probed <- err
	}()
	<-entered
	for i := range 7 {
		if _, err := g.Ask(context.Background(), src, q); !errors.Is(err, ErrResting) {
			t.Fatalf("caller %d during the probe: %v", i, err)
		}
	}
	close(release)
	if err := <-probed; !errors.Is(err, decide.ErrUpstream) {
		t.Fatalf("the probe: %v", err)
	}
	if got := up.calls.Load(); got != RestAfter+1 {
		t.Fatalf("%d calls reached the route while it was being probed; want one", int(got)-RestAfter)
	}
}

type askerFunc func(context.Context, decide.Source, decide.Question) (rpc.DecideResponse, error)

func (f askerFunc) Ask(ctx context.Context, s decide.Source, q decide.Question) (rpc.DecideResponse, error) {
	return f(ctx, s, q)
}

type records struct {
	mu   sync.Mutex
	msgs []string
}

func (r *records) Enabled(context.Context, slog.Level) bool { return true }
func (r *records) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, rec.Level.String()+" "+rec.Message)
	return nil
}
func (r *records) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *records) WithGroup(string) slog.Handler      { return r }

func TestTheLogSaysEachTransitionOnce(t *testing.T) {
	logged := &records{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logged))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b, c := testBreaker()
	up := &routeAsker{}
	up.fail(quota)
	src := routed("rou2", "gpt-6-luna")
	runs(t, b.Guard(up), src, 20)
	for range 4 {
		c.pass(LongRest)
		runs(t, b.Guard(up), src, 5)
	}
	up.heal()
	c.pass(LongRest)
	runs(t, b.Guard(up), src, 5)

	var routeLines []string
	for _, m := range logged.msgs {
		if strings.Contains(m, "decision route") {
			routeLines = append(routeLines, m)
		}
	}
	want := []string{"WARN decision route resting: methods write their defaults without asking", "INFO decision route answering again"}
	if strings.Join(routeLines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("log lines:\n%s\nwant:\n%s", strings.Join(routeLines, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheLastErrorIsShortAndPrintable(t *testing.T) {
	b, _ := testBreaker()
	up := &routeAsker{}
	up.fail(fmt.Errorf("%w: decide: HTTP 429: \x1b]52;c;cGF3bmVk\x07%s", decide.ErrUpstream, strings.Repeat("é", 400)))
	runs(t, b.Guard(up), routed("rou2", "gpt-6-luna"), RestAfter)
	r := b.Resting()
	if len(r) != 1 {
		t.Fatalf("resting %+v", r)
	}
	last := r[0].LastError
	if strings.ContainsAny(last, "\x1b\x07") || len(last) > errBytes+len("…") || !strings.HasSuffix(last, "…") {
		t.Fatalf("last error %q", last)
	}
	if !strings.HasPrefix(r[0].Route, "rou2/gpt-6-luna") {
		t.Fatalf("route %q", r[0].Route)
	}
}

func TestANilBreakerAsksEveryTime(t *testing.T) {
	var b *Breaker
	up := &routeAsker{}
	up.fail(quota)
	runs(t, b.Guard(up), routed("rou2", "gpt-6-luna"), 2*RestAfter)
	if up.calls.Load() != 2*RestAfter || b.Resting() != nil {
		t.Fatal("a nil breaker must pass every call through")
	}
}
