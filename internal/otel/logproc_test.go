package otel

import (
	"context"
	"sync"
	"testing"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
)

type slowExporter struct {
	mu      sync.Mutex
	got     int
	release chan struct{}
}

func (e *slowExporter) Export(_ context.Context, recs []sdklog.Record) error {
	if e.release != nil {
		<-e.release
	}
	e.mu.Lock()
	e.got += len(recs)
	e.mu.Unlock()
	return nil
}
func (e *slowExporter) Shutdown(context.Context) error   { return nil }
func (e *slowExporter) ForceFlush(context.Context) error { return nil }
func (e *slowExporter) count() int                       { e.mu.Lock(); defer e.mu.Unlock(); return e.got }

// A wedged collector must not stall the caller: OnEmit returns while the
// exporter is blocked, and the ring sheds instead of growing.
func TestBoundedProcessorDropsRatherThanBlocks(t *testing.T) {
	exp := &slowExporter{release: make(chan struct{})}
	p := newBoundedProcessor(exp, 8)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			var r sdklog.Record
			r.SetSeverity(9)
			_ = p.OnEmit(context.Background(), &r)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnEmit blocked behind a wedged exporter")
	}
	if p.Dropped() == 0 {
		t.Fatal("a full ring must shed and count")
	}
	close(exp.release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Capacity is the policy: 0 is the file sink's inline export.
func TestZeroCapacityIsSynchronous(t *testing.T) {
	exp := &slowExporter{}
	p := newLogProcessor(exp, 0)
	var r sdklog.Record
	r.SetSeverity(9)
	if err := p.OnEmit(context.Background(), &r); err != nil {
		t.Fatal(err)
	}
	if exp.count() != 1 {
		t.Fatalf("inline export expected, exporter saw %d", exp.count())
	}
}

// Shutdown keeps the last seconds.
func TestShutdownFlushes(t *testing.T) {
	exp := &slowExporter{}
	p := newBoundedProcessor(exp, 64)
	for i := 0; i < 10; i++ {
		var r sdklog.Record
		r.SetSeverity(9)
		_ = p.OnEmit(context.Background(), &r)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exp.count() != 10 {
		t.Fatalf("shutdown lost records: %d of 10", exp.count())
	}
}
