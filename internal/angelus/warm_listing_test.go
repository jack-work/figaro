package angelus

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/store"
)

type warmRecorder struct {
	mu       sync.Mutex
	meta     map[string]int
	lastTS   map[string]int
	inFlight int
	peak     int
	hold     time.Duration
}

func (w *warmRecorder) enter(m map[string]int, id string) {
	w.mu.Lock()
	m[id]++
	w.inFlight++
	w.peak = max(w.peak, w.inFlight)
	w.mu.Unlock()
	time.Sleep(w.hold)
	w.mu.Lock()
	w.inFlight--
	w.mu.Unlock()
}

func (w *warmRecorder) Meta(id string) (*store.AriaMeta, error) {
	w.enter(w.meta, id)
	return nil, nil
}

func (w *warmRecorder) LastTS(id string) int64 {
	w.enter(w.lastTS, id)
	return 0
}

func warmNodes(n int) []store.NodeView {
	nodes := make([]store.NodeView, n)
	for i := range nodes {
		nodes[i] = store.NodeView{ID: fmt.Sprintf("n%04d", i), Kind: conversationKind}
		if i%7 == 3 {
			nodes[i].Kind = "outfit"
		}
	}
	return nodes
}

func TestTheParallelWarmWalkTouchesEveryConversationOnce(t *testing.T) {
	nodes := warmNodes(503)
	for _, workers := range []int{1, 4, 64, 1000} {
		w := &warmRecorder{meta: map[string]int{}, lastTS: map[string]int{}}
		warmRows(context.Background(), w, nodes, workers)
		want := map[string]int{}
		for _, n := range nodes {
			if n.Kind == conversationKind {
				want[n.ID] = 1
			}
		}
		if !reflect.DeepEqual(w.meta, want) {
			t.Fatalf("workers=%d: Meta visits differ from one per conversation (%d visited, %d wanted)", workers, len(w.meta), len(want))
		}
		if !reflect.DeepEqual(w.lastTS, want) {
			t.Fatalf("workers=%d: LastTS visits differ from one per conversation (%d visited, %d wanted)", workers, len(w.lastTS), len(want))
		}
	}
}

func TestTheWarmWalkRunsItsWorkersAtOnce(t *testing.T) {
	w := &warmRecorder{meta: map[string]int{}, lastTS: map[string]int{}, hold: 5 * time.Millisecond}
	warmRows(context.Background(), w, warmNodes(64), 4)
	if w.peak < 2 {
		t.Fatalf("at most %d call in flight with 4 workers: the walk is serial", w.peak)
	}
	if w.peak > 4 {
		t.Fatalf("%d calls in flight with 4 workers: the walk is unbounded", w.peak)
	}
}

func TestACancelledWarmWalkStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &warmRecorder{meta: map[string]int{}, lastTS: map[string]int{}}
	warmRows(ctx, w, warmNodes(100), 4)
	if len(w.meta) != 0 {
		t.Fatalf("a cancelled walk still read %d metas", len(w.meta))
	}
}

func TestAnEmptyStoreWarmsWithoutWorkers(t *testing.T) {
	warmRows(context.Background(), &warmRecorder{}, nil, 8)
}
