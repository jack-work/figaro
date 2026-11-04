package angelus

import (
	"context"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// PprofEnv arms the profiler. Off by default: a profile stops the world and
// /debug/pprof/cmdline leaks argv.
const PprofEnv = "FIGARO_PPROF"

const PprofSocketName = "pprof.sock"

const UnlimitedMemLimit int64 = math.MaxInt64

func (a *Angelus) PprofSocketPath() string {
	dir := a.pprofDir
	if dir == "" {
		dir = filepath.Clean(a.RuntimeDir) + "-pprof"
	}
	return filepath.Join(dir, PprofSocketName)
}

func (a *Angelus) StartPprof(ctx context.Context) error {
	a.pprof.mu.Lock()
	a.pprof.life = ctx
	a.pprof.mu.Unlock()
	if os.Getenv(PprofEnv) == "" {
		return nil
	}
	_, err := a.ArmPprof(true)
	return err
}

type pprofState struct {
	mu   sync.Mutex
	life context.Context
	srv  *http.Server
	stop chan struct{}
	path string
}

func (a *Angelus) pprofSocket() string {
	a.pprof.mu.Lock()
	defer a.pprof.mu.Unlock()
	return a.pprof.path
}

func (a *Angelus) ArmPprof(on bool) (string, error) {
	p := &a.pprof
	p.mu.Lock()
	defer p.mu.Unlock()
	if !on {
		if p.srv != nil {
			p.srv.Close()
			close(p.stop)
			os.Remove(p.path)
			runtime.SetMutexProfileFraction(0)
			runtime.SetBlockProfileRate(0)
			slog.Info("pprof disarmed", "socket", p.path)
		}
		p.srv, p.stop, p.path = nil, nil, ""
		return "", nil
	}
	if p.srv != nil {
		return p.path, nil
	}
	path := a.PprofSocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Warn("pprof socket dir unavailable", "path", path, "err", err)
		return "", err
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		slog.Warn("pprof socket unavailable", "path", path, "err", err)
		return "", err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		slog.Warn("pprof socket chmod failed", "path", path, "err", err)
		return "", err
	}

	mux := http.NewServeMux()
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(10000)
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{Handler: mux}
	life := p.life
	if life == nil {
		life = context.Background()
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-life.Done():
			a.ArmPprof(false)
		case <-stop:
		}
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("pprof server stopped", "err", err)
		}
	}()
	p.srv, p.stop, p.path = srv, stop, path
	slog.Info("pprof armed", "socket", path)
	return path, nil
}

// MemStatus stops the world (ReadMemStats); keep it off tickers.
func (a *Angelus) MemStatus() *rpc.MemStatus {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	st := &rpc.MemStatus{
		LiveArias:         a.Registry.FigaroCount(),
		Goroutines:        runtime.NumGoroutine(),
		HeapAllocBytes:    ms.HeapAlloc,
		HeapInuseBytes:    ms.HeapInuse,
		HeapIdleBytes:     ms.HeapIdle,
		HeapReleasedBytes: ms.HeapReleased,
		HeapSysBytes:      ms.HeapSys,
		SysBytes:          ms.Sys,
		NumGC:             ms.NumGC,
		MemLimitBytes:     debug.SetMemoryLimit(-1),
		PprofSocket:       a.pprofSocket(),
	}
	a.fillFormMem(st)
	if a.Sessions != nil {
		st.Sessions = a.Sessions.Count()
	}
	if ev, ok := a.Backend.(idleEvictor); ok {
		st.ResidentArias = ev.Resident()
	}
	if tr, ok := a.Backend.(residentTrimmer); ok {
		st.ResidentIRRows = tr.ResidentRows()
		st.ResidentIRBytes = tr.ResidentIRBytes()
	}
	if fp, ok := a.Backend.(interface{ ResidentFormPatches() int }); ok {
		st.ResidentFormPatches = fp.ResidentFormPatches()
	}
	if pr, ok := a.Backend.(interface {
		PendingRemovals() []store.PendingRemoval
	}); ok {
		pending := pr.PendingRemovals()
		st.PendingRemovals = len(pending)
		for _, p := range pending {
			st.PendingRemovalHeads += p.Heads
		}
		if len(pending) > 0 {
			st.OldestPendingRemovalMS = time.Since(pending[0].Since).Milliseconds()
		}
	}
	if lh, ok := a.Backend.(interface{ LoadedHeads() int }); ok {
		st.LoadedHeads = lh.LoadedHeads()
	}
	if sc, ok := a.Backend.(interface {
		SegmentCacheBytes() int64
		SegmentCacheBudget() int64
		SegmentCacheLoads() int64
	}); ok {
		st.SegmentCacheBytes = sc.SegmentCacheBytes()
		st.SegmentCacheBudget = sc.SegmentCacheBudget()
		st.SegmentCacheLoads = sc.SegmentCacheLoads()
	}
	if lb, ok := a.Backend.(interface{ LibrettoStats() (int, int) }); ok {
		st.Librettos, st.LibrettoObservers = lb.LibrettoStats()
	}
	if a.UICache != nil {
		var ev int64
		st.UIWindowBytes, st.UIWindowBudget, ev = a.UICache.Budget().Stats()
		st.UIWindowEvictions = int(ev)
	}
	if sweep := a.lastLibrettoSweep.Load(); sweep != nil {
		st.LibrettoSweepMinted = sweep.Minted
		st.LibrettoSweepCorrected = sweep.Corrected
		st.LibrettoSweepMissing = sweep.Missing
	}
	if tr, ok := a.Backend.(interface {
		ResidentTranslationRows() int
		ResidentTranslationBytes() int
	}); ok {
		st.ResidentTranslationRows = tr.ResidentTranslationRows()
		st.ResidentTranslationBytes = tr.ResidentTranslationBytes()
	}
	if a.Hubs != nil {
		for _, hb := range a.Hubs.all() {
			st.Endpoints++
			st.AttachedClients += hb.Attached()
		}
	}
	return st
}

// MemCollectStatus stops the world twice (GC, then ReadMemStats), which is
// why it is not a flag on the status call.
func (a *Angelus) MemCollectStatus() *rpc.MemStatus {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	start := time.Now()
	runtime.GC()
	took := time.Since(start)

	runtime.ReadMemStats(&after)

	st := a.MemStatus()
	st.HeapAllocBytes = after.HeapAlloc
	st.HeapInuseBytes = after.HeapInuse
	st.HeapIdleBytes = after.HeapIdle
	st.HeapReleasedBytes = after.HeapReleased
	st.HeapSysBytes = after.HeapSys
	st.SysBytes = after.Sys
	st.NumGC = after.NumGC

	var reclaimed uint64
	if before.HeapAlloc > after.HeapAlloc {
		reclaimed = before.HeapAlloc - after.HeapAlloc
	}
	var released uint64
	if after.HeapReleased > before.HeapReleased {
		released = after.HeapReleased - before.HeapReleased
	}
	st.Collected = &rpc.Collection{
		BeforeBytes:   before.HeapAlloc,
		AfterBytes:    after.HeapAlloc,
		ReclaimedByes: reclaimed,
		ReleasedDelta: released,
		Took:          took,
	}
	return st
}
