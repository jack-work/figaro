package angelus

import (
	"context"
	"encoding/json"
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

func (f *fakeQuietStore) NormalizePending() bool { return f.pending }

func (f *fakeQuietStore) Normalize(ctx context.Context, dryRun bool) (store.NormalizeReport, error) {
	if dryRun {
		panic("the quiet pass must not dry-run")
	}
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
