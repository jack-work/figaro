package store

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stalledMarkEdit parks the first mark edit between its append and its
// publish: the record is on disk, the form has not moved, and the edit's
// commit sinks have not run.
type stalledMarkEdit struct {
	*xwalFormLog
	armed   atomic.Bool
	once    sync.Once
	entered chan struct{}
	gate    chan struct{}
}

func (l *stalledMarkEdit) AppendPatch(payload []byte) (uint64, error) {
	var rec formRecord
	if json.Unmarshal(payload, &rec) == nil {
		for _, e := range rec.Patch.Entries() {
			if strings.HasPrefix(e.Key, KeyCheckpointMarks) {
				l.armed.Store(true)
			}
		}
	}
	return l.xwalFormLog.AppendPatch(payload)
}

func (l *stalledMarkEdit) SyncThrough(index uint64) error {
	if l.armed.Load() {
		l.once.Do(func() {
			l.entered <- struct{}{}
			<-l.gate
		})
	}
	return l.xwalFormLog.SyncThrough(index)
}

func openStalled(t *testing.T, b *XwalBackend, id string) (*Form, *stalledMarkEdit) {
	t.Helper()
	b.dropForm(id)
	l := &stalledMarkEdit{xwalFormLog: &xwalFormLog{backend: b, ariaID: id}, entered: make(chan struct{}, 1), gate: make(chan struct{})}
	opened, err := OpenForm(l)
	if err != nil {
		t.Fatal(err)
	}
	f, err := b.registerForm(id, opened)
	if err != nil || f != opened {
		t.Fatalf("fixture: the stalled form is not %s's: %v", id, err)
	}
	return f, l
}

// writeUntilATailMark writes just enough for the last write to make a tail
// mark due: a write queued behind the stalled edit would never return.
func writeUntilATailMark(t *testing.T, b *XwalBackend, f *Form, every int) {
	t.Helper()
	n := every - int(f.Read().Version)
	if n <= 0 {
		t.Fatalf("fixture: version %d is already past a tail mark at %d", f.Read().Version, every)
	}
	writeBoard(t, b, f.log.(*stalledMarkEdit).ariaID, 0, n)
}

// Eviction closes forms under the backend's lock, and a tail mark in flight
// finishes through commit sinks that take that lock: a Close that waited for
// the checkpoint would hang the backend.
func TestEvictionDoesNotWaitOnAMarkEditInFlight(t *testing.T) {
	checkpointEvery(t, 64, 5)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	id := mustNewAriaTB(t, b)
	f, stall := openStalled(t, b, id)
	writeUntilATailMark(t, b, f, 64)
	<-stall.entered
	evicted := make(chan struct{})
	go func() {
		b.EvictIdle(nil, 0)
		close(evicted)
	}()
	select {
	case <-evicted:
	case <-time.After(2 * time.Second):
		t.Fatal("evicting a form with a tail mark in flight hung the backend")
	}
	close(stall.gate)
	f.CloseAndSettle()
	b.Close()
}

// A tail mark is a record on the parent written in the background. A head
// fork that lands while one is between its append and its publish still
// starts from the parent's resident state: the fork waits for the mark
// rather than inheriting a version the parent has not reached.
func TestAMarkEditInFlightDoesNotSendAHeadForkToAReplay(t *testing.T) {
	checkpointEvery(t, 64, 5)
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parent := mustNewAriaTB(t, b)
	logCanary(t, b, parent)
	pf, stall := openStalled(t, b, parent)
	plantCanary(t, pf)
	writeUntilATailMark(t, b, pf, 64)
	<-stall.entered

	type forked struct {
		child string
		err   error
	}
	done := make(chan forked, 1)
	go func() {
		child, _, err := b.ForkWith(parent, 0, birth("c"))
		done <- forked{child, err}
	}()
	var f forked
	select {
	case f = <-done:
		close(stall.gate)
	case <-time.After(200 * time.Millisecond):
		close(stall.gate)
		f = <-done
	}
	if f.err != nil {
		t.Fatal(f.err)
	}
	cf := residentForm(b, f.child)
	if cf == nil || canaryOf(t, cf) != canaryPlanted {
		t.Fatal("the fork was replayed from its log: a tail mark in flight moved the parent under it")
	}
}
