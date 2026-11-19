package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type fakeQuietStore struct {
	store.Backend
	pending bool
	started chan time.Time
	stopped chan time.Time
	block   bool
}

func (f *fakeQuietStore) NormalizePending(func(string) bool) bool { return f.pending }

func (f *fakeQuietStore) NormalizeSkipping(ctx context.Context, _ func(string) bool) (store.NormalizeReport, error) {
	f.started <- time.Now()
	if f.block {
		<-ctx.Done()
		f.stopped <- time.Now()
		return store.NormalizeReport{Remaining: []string{"a"}}, ctx.Err()
	}
	f.pending = false
	return store.NormalizeReport{Detached: []string{"a"}}, nil
}

func quietAngelus(t *testing.T, f *fakeQuietStore, seconds int) *Angelus {
	t.Helper()
	settings := &config.Loaded{Config: config.Config{Store: config.StoreConfig{NormalizeAfterQuietSeconds: &seconds}}}
	return New(Config{RuntimeDir: t.TempDir(), Backend: f, Settings: settings})
}

func call(a *Angelus, method string) {
	noop := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	a.joined(method, noop)(context.Background(), nil)
}

func TestQuietNormalizeWaitsOutTheQuietPeriod(t *testing.T) {
	f := &fakeQuietStore{pending: true, started: make(chan time.Time, 1)}
	a := quietAngelus(t, f, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.normalizeWhenQuiet(ctx)
	time.Sleep(500 * time.Millisecond)
	call(a, rpc.MethodSet)
	worked := time.Now()
	time.Sleep(500 * time.Millisecond)
	call(a, rpc.MethodRead)
	call(a, rpc.MethodStatus)
	select {
	case at := <-f.started:
		if waited := at.Sub(worked); waited < time.Second || waited > 3*time.Second {
			t.Fatalf("the pass started %v after the last work call, want one quiet second from it, reads not counting", waited)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no pass after a quiet second")
	}
}

func TestQuietNormalizeIsOffAtZero(t *testing.T) {
	f := &fakeQuietStore{pending: true, started: make(chan time.Time, 1)}
	a := quietAngelus(t, f, 0)
	done := make(chan struct{})
	go func() {
		a.normalizeWhenQuiet(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("normalize_after_quiet_seconds = 0 still runs the quiet loop")
	}
}

// Defends "cancellable the moment work arrives": a read leaves the pass
// running, a call that does work stops it before it returns.
func TestWorkStopsAQuietNormalizeAndReadsDoNot(t *testing.T) {
	f := &fakeQuietStore{pending: true, block: true, started: make(chan time.Time, 1), stopped: make(chan time.Time, 1)}
	a := quietAngelus(t, f, 1)
	a.activity.last.Store(time.Now().Add(-time.Hour).UnixNano())
	passDone := make(chan struct{})
	go func() {
		a.normalizeQuietly(context.Background(), f, time.Second)
		close(passDone)
	}()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass never started")
	}
	call(a, rpc.MethodRead)
	call(a, rpc.MethodList)
	select {
	case <-f.stopped:
		t.Fatal("a read stopped the quiet pass")
	case <-time.After(200 * time.Millisecond):
	}
	sent := time.Now()
	call(a, rpc.MethodQua)
	select {
	case at := <-f.stopped:
		t.Logf("pass stopped %v after the work call", at.Sub(sent))
	case <-time.After(time.Second):
		t.Fatal("a send did not stop the quiet pass")
	}
	<-passDone
	if a.activity.cancel.Load() != nil {
		t.Fatal("the finished pass left its cancel armed")
	}
}

// Defends the admission cache through an unattended pass: a detach moves a
// figaro's history and never where it is drawn, so no cached fan-out count
// may change under it.
func TestAQuietNormalizeLeavesEveryFigaroWhereItIsDrawn(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	other, _ := b.CreateConversation(outfit)
	var kids []string
	for range 3 {
		out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
		if err != nil {
			t.Fatal(err)
		}
		kids = append(kids, out.Alternative)
	}
	if err := hs.h.place(inProcess(context.Background()), kids[0], other); err != nil {
		t.Fatal(err)
	}
	callDemote(t, hs, rpc.DemoteRequest{FigaroID: top, Prefer: kids[1]})
	requireCacheMatchesTree(t, hs, "a place and a demote")
	if !b.NormalizePending(nil) {
		t.Fatal("nothing is drawn away from its history; the test proves nothing")
	}
	a := hs.h.angelus
	everyone := append([]string{top, other}, kids...)
	drawn, counts := map[string]string{}, map[string]int{}
	for _, id := range everyone {
		drawn[id], counts[id] = a.drawnParentOf(id), a.liveChildCount(id)
	}
	a.normalizeQuietly(context.Background(), b, time.Nanosecond)
	if b.NormalizePending(nil) {
		t.Fatal("the quiet pass left a figaro to detach")
	}
	requireCacheMatchesTree(t, hs, "a quiet normalize")
	for _, id := range everyone {
		if got := a.drawnParentOf(id); got != drawn[id] {
			t.Errorf("%s is drawn under %q after the pass, %q before", id, got, drawn[id])
		}
		if got := a.liveChildCount(id); got != counts[id] {
			t.Errorf("%s holds %d live children after the pass, %d before", id, got, counts[id])
		}
	}
}

type failingQuietStore struct {
	store.Backend
	left     []string
	bad      string
	attempts int
}

func (f *failingQuietStore) NormalizePending(skip func(string) bool) bool {
	for _, id := range f.left {
		if !skip(id) {
			return true
		}
	}
	return false
}

func (f *failingQuietStore) NormalizeSkipping(_ context.Context, skip func(string) bool) (store.NormalizeReport, error) {
	var r store.NormalizeReport
	var err error
	for _, id := range slices.Clone(f.left) {
		switch {
		case skip(id):
		case id == f.bad:
			f.attempts++
			r.Failed = append(r.Failed, id)
			err = errors.New("normalize: the carry refused")
		default:
			r.Detached = append(r.Detached, id)
			f.left = slices.DeleteFunc(f.left, func(s string) bool { return s == id })
		}
	}
	return r, err
}

// Defends the unattended pass against a figaro that never detaches: the
// figaros after it still detach, and it is not copied again every quiet
// minute but waits out a backoff that grows with each failure.
func TestAFigaroThatWillNotDetachWaitsOutABackoff(t *testing.T) {
	f := &failingQuietStore{left: []string{"a-bad", "b-good"}, bad: "a-bad"}
	a := quietAngelus(t, &fakeQuietStore{}, 1)
	a.activity.last.Store(time.Now().Add(-time.Hour).UnixNano())
	a.normalizeQuietly(context.Background(), f, time.Second)
	if f.attempts != 1 || !slices.Equal(f.left, []string{"a-bad"}) {
		t.Fatalf("after one pass: %d attempts at the bad figaro, %v left; want 1 and only it", f.attempts, f.left)
	}
	first := a.activity.failed["a-bad"]
	if wait := time.Until(first.until); wait < quietFailBackoff-time.Minute || wait > quietFailBackoff {
		t.Fatalf("the first failure waits %v, want about %v", wait, quietFailBackoff)
	}
	if f.NormalizePending(a.activity.failed.waiting(time.Now())) {
		t.Fatal("a figaro in its backoff still reads as pending work")
	}
	a.normalizeQuietly(context.Background(), f, time.Second)
	if f.attempts != 1 {
		t.Fatalf("a pass inside the backoff tried the bad figaro again (%d attempts)", f.attempts)
	}
	first.until = time.Now().Add(-time.Second)
	a.activity.failed["a-bad"] = first
	a.normalizeQuietly(context.Background(), f, time.Second)
	if f.attempts != 2 {
		t.Fatalf("a pass after the backoff made %d attempts in all, want 2", f.attempts)
	}
	if wait := time.Until(a.activity.failed["a-bad"].until); wait < 2*quietFailBackoff-time.Minute {
		t.Fatalf("the second failure waits %v, want the backoff doubled", wait)
	}
}
