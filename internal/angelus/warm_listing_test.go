package angelus

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro/wire"
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

// On a real store snapshot, the parallel walk leaves the backend answering
// exactly what the serial walk does.
//
//	FIGARO_WARM_SNAPSHOT=/var/tmp/perf/startup-metrics/stores/big/arias go test ./internal/angelus -run Snapshot -v
func TestTheParallelWarmWalkMatchesTheSerialOneOnASnapshot(t *testing.T) {
	src := os.Getenv("FIGARO_WARM_SNAPSHOT")
	if src == "" {
		t.Skip("set FIGARO_WARM_SNAPSHOT to an arias directory (reflink-copied twice, never opened in place)")
	}
	open := func(name string) *store.XwalBackend {
		dir, err := os.MkdirTemp("/var/tmp", "figaro-warm-"+name+"-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		root := filepath.Join(dir, "arias")
		if out, err := exec.Command("cp", "-a", "--reflink=auto", src, root).CombinedOutput(); err != nil {
			t.Fatalf("copy snapshot: %v\n%s", err, out)
		}
		b, err := store.NewXwalBackend(root, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { b.Close() })
		if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
			t.Fatal(err)
		}
		return b
	}
	serial, parallel := open("serial"), open("parallel")

	walk := func(b *store.XwalBackend, workers int) ([]store.NodeView, time.Duration) {
		nodes := b.Conversations()
		start := time.Now()
		warmRows(context.Background(), b, nodes, workers)
		return nodes, time.Since(start)
	}
	sNodes, sTook := walk(serial, 1)
	pNodes, pTook := walk(parallel, runtime.GOMAXPROCS(0))
	t.Logf("serial %s, %d workers %s", sTook, runtime.GOMAXPROCS(0), pTook)

	type row struct {
		meta   *store.AriaMeta
		err    string
		lastTS int64
	}
	rows := func(b *store.XwalBackend, nodes []store.NodeView) map[string]row {
		out := map[string]row{}
		for _, n := range nodes {
			if n.Kind != conversationKind {
				continue
			}
			m, err := b.Meta(n.ID)
			r := row{meta: m, lastTS: b.LastTS(n.ID)}
			if err != nil {
				r.err = err.Error()
			}
			out[n.ID] = r
		}
		return out
	}
	want, got := rows(serial, sNodes), rows(parallel, pNodes)
	if len(want) < 1000 {
		t.Fatalf("only %d conversations in the snapshot: this proves little", len(want))
	}
	if len(got) != len(want) {
		t.Fatalf("parallel walk sees %d conversations, serial %d", len(got), len(want))
	}
	for id, w := range want {
		if g := got[id]; !reflect.DeepEqual(g, w) {
			t.Fatalf("%s differs:\nserial   %+v %+v\nparallel %+v %+v", id, w, w.meta, g, g.meta)
		}
	}
	t.Logf("%d conversations equal", len(want))
}
