package angelus_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

// The Snapshot benchmarks run the daemon over a copy of a real store, so a
// change to forking or to the hierarchy is measured at the size it ships to.
// FIGARO_BENCH_SNAPSHOT names the directory holding arias/; the copy is a
// reflink, made beside it or under FIGARO_BENCH_SCRATCH (same filesystem).
//
//	go test ./internal/angelus -run '^$' -bench 'SnapshotBoot' -benchtime=1x -count=10
//	go test ./internal/angelus -run '^$' -bench 'Snapshot(List|PromoteDemote)' -benchtime=200x -count=10
//	go test ./internal/angelus -run '^$' -bench 'Snapshot(Fork|FirstSend|KillRecursive)' -benchtime=20x -count=10
type snapshotRig struct {
	a       *angelus.Angelus
	acli    *sdk.Angelus
	ctx     context.Context
	backend *store.XwalBackend
}

func snapshotCopy(b *testing.B) string {
	b.Helper()
	src := os.Getenv("FIGARO_BENCH_SNAPSHOT")
	if src == "" {
		b.Skip("FIGARO_BENCH_SNAPSHOT names the store to copy")
	}
	scratch := os.Getenv("FIGARO_BENCH_SCRATCH")
	if scratch == "" {
		scratch = filepath.Dir(src)
	}
	dir, err := os.MkdirTemp(scratch, "snapshot-bench-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	if out, err := exec.Command("cp", "-a", "--reflink=auto", filepath.Join(src, "arias"), filepath.Join(dir, "arias")).CombinedOutput(); err != nil {
		b.Fatalf("copy %s: %v: %s", src, err, out)
	}
	if err := os.MkdirAll(filepath.Join(dir, "outfits"), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outfits", "mock.toml"), []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("default_outfit = \"mock\"\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	return dir
}

func quiet(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(old) })
}

func snapshotDaemon(b *testing.B, dir string) *snapshotRig {
	b.Helper()
	s := &snapshotRig{}
	s.a, s.acli, s.ctx = daemonOnStore(b, dir, func(_ *angelus.Angelus, be *store.XwalBackend) { s.backend = be })
	return s
}

// deepest lists conversations by how many conversations their history runs
// through, deepest first: a fork at turn 1 of one of these is cut inside an
// ancestor's turns.
func (s *snapshotRig) deepest() []string {
	nodes := s.backend.Store().Nodes()
	parent := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.Kind == string(store.KindConversation) {
			parent[n.ID] = n.Parent
		}
	}
	depth := func(id string) int {
		d := 0
		for up, ok := parent[id]; ok && d < len(parent); up, ok = parent[up] {
			d++
		}
		return d
	}
	ids := make([]string, 0, len(parent))
	for id := range parent {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if di, dj := depth(ids[i]), depth(ids[j]); di != dj {
			return di > dj
		}
		return ids[i] < ids[j]
	})
	return ids
}

// lead is the deepest conversation the daemon will fork at turn 1.
func (s *snapshotRig) lead(b *testing.B) string {
	b.Helper()
	for i, id := range s.deepest() {
		if i == 50 {
			break
		}
		if _, err := s.acli.Fork(s.ctx, id, 1, 0, []string{"mock"}, nil); err == nil {
			return id
		}
	}
	b.Fatal("no conversation among the 50 deepest forks at turn 1")
	return ""
}

func (s *snapshotRig) fork(b *testing.B, id string, turn uint64) string {
	b.Helper()
	resp, err := s.acli.Fork(s.ctx, id, turn, 0, []string{"mock"}, nil)
	if err != nil {
		b.Fatal(err)
	}
	return resp.Alternative
}

// A figaro's allocation counters are the whole process's, and a daemon
// that has just booted or just answered is still working. Every measured
// operation therefore starts from an idle daemon, with the clock and the
// counters stopped while it waits: allocs/op and B/op are then what the
// operation itself costs, on both sides of the socket.
func idleDaemon(b *testing.B) {
	b.Helper()
	mallocs := func() uint64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.Mallocs
	}
	deadline := time.Now().Add(30 * time.Second)
	for last := mallocs(); ; {
		time.Sleep(100 * time.Millisecond)
		now := mallocs()
		if now-last < 16 {
			return
		}
		if time.Now().After(deadline) {
			b.Fatal("the daemon never went idle: its allocations would be counted against the operation")
		}
		last = now
	}
}

func fromIdle(b *testing.B) {
	b.Helper()
	b.StopTimer()
	idleDaemon(b)
	b.StartTimer()
}

func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// One cold start per iteration: run it with -benchtime=1x. open-ms is the
// store alone, list-ms the first listing, fork-ms the first fork at turn 1,
// and heap-MB what the daemon holds once those have filled its caches.
func BenchmarkSnapshotBoot(b *testing.B) {
	quiet(b)
	for b.Loop() {
		b.StopTimer()
		dir := snapshotCopy(b)
		before := heapMB()
		b.StartTimer()

		t0 := time.Now()
		be, err := store.NewXwalBackend(filepath.Join(dir, "arias"), 0)
		if err != nil {
			b.Fatal(err)
		}
		open := time.Since(t0)
		if err := be.Close(); err != nil {
			b.Fatal(err)
		}

		s := snapshotDaemon(b, dir)
		t1 := time.Now()
		if _, err := s.acli.List(s.ctx); err != nil {
			b.Fatal(err)
		}
		list := time.Since(t1)
		t2 := time.Now()
		s.lead(b)
		fork := time.Since(t2)

		b.StopTimer()
		b.ReportMetric(float64(open.Microseconds())/1000, "open-ms")
		b.ReportMetric(float64(list.Microseconds())/1000, "list-ms")
		b.ReportMetric(float64(fork.Microseconds())/1000, "fork-ms")
		b.ReportMetric(heapMB()-before, "heap-MB")
		s.a.Shutdown(0)
		b.StartTimer()
	}
}

func BenchmarkSnapshotList(b *testing.B) {
	quiet(b)
	s := snapshotDaemon(b, snapshotCopy(b))
	if _, err := s.acli.List(s.ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		fromIdle(b)
		if _, err := s.acli.List(s.ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// cut=head forks the lead at its tail. cut=turn1 forks it inside an
// ancestor's turns, the fork that is placed under the figaro it was asked of.
func BenchmarkSnapshotFork(b *testing.B) {
	quiet(b)
	for _, cut := range []struct {
		name string
		turn uint64
	}{{"cut=head", 0}, {"cut=turn1", 1}} {
		b.Run(cut.name, func(b *testing.B) {
			s := snapshotDaemon(b, snapshotCopy(b))
			lead := s.lead(b)
			b.ReportAllocs()
			for b.Loop() {
				fromIdle(b)
				s.fork(b, lead, cut.turn)
			}
		})
	}
}

// The first turn of a fork cut at turn 1: restore, catch-up on a provider the
// history has no rows for, and one mock reply.
func BenchmarkSnapshotFirstSendAfterFork(b *testing.B) {
	quiet(b)
	s := snapshotDaemon(b, snapshotCopy(b))
	lead := s.lead(b)
	for b.Loop() {
		b.StopTimer()
		alt := s.fork(b, lead, 1)
		done := make(chan struct{}, 1)
		fcli, err := sdk.DialAria(ariaAddr(s.a, alt), func(method string, _ json.RawMessage) {
			if method == "turn.done" {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
		if err != nil {
			b.Fatal(err)
		}
		idleDaemon(b)
		b.StartTimer()
		if _, _, err := fcli.Qua(s.ctx, "first prompt", nil); err != nil {
			b.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			b.Fatal("the first turn never finished")
		}
		b.StopTimer()
		fcli.Close()
		waitIdle(b, s.a, alt)
		b.StartTimer()
	}
}

// A promote and the demote that undoes it: two moves through the hierarchy.
func BenchmarkSnapshotPromoteDemote(b *testing.B) {
	quiet(b)
	s := snapshotDaemon(b, snapshotCopy(b))
	lead := s.lead(b)
	alt := s.fork(b, lead, 1)
	b.ReportAllocs()
	for b.Loop() {
		fromIdle(b)
		if _, err := s.acli.Promote(s.ctx, alt, 1); err != nil {
			b.Fatal(err)
		}
		if _, err := s.acli.Demote(s.ctx, alt, lead); err != nil {
			b.Fatal(err)
		}
	}
}

// kill -r of a fork cut at turn 1 that has three forks of its own.
func BenchmarkSnapshotKillRecursive(b *testing.B) {
	quiet(b)
	s := snapshotDaemon(b, snapshotCopy(b))
	lead := s.lead(b)
	for b.Loop() {
		b.StopTimer()
		alt := s.fork(b, lead, 1)
		for range 3 {
			s.fork(b, alt, 0)
		}
		idleDaemon(b)
		b.StartTimer()
		if _, err := s.acli.Kill(s.ctx, rpc.KillRequest{FigaroID: alt, Recursive: true, Now: true}); err != nil {
			b.Fatal(err)
		}
	}
}
