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
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

// PprofEnv arms the profiler. Off by default: a profile stops the world and
// /debug/pprof/cmdline leaks argv.
const PprofEnv = "FIGARO_PPROF"

const PprofSocketName = "pprof.sock"

const UnlimitedMemLimit int64 = math.MaxInt64

func (a *Angelus) PprofSocketPath() string {
	return filepath.Join(a.RuntimeDir, PprofSocketName)
}

func (a *Angelus) StartPprof(ctx context.Context) error {
	if os.Getenv(PprofEnv) == "" {
		return nil
	}
	path := a.PprofSocketPath()
	os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		slog.Warn("pprof socket unavailable", "path", path, "err", err)
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		slog.Warn("pprof socket chmod failed", "path", path, "err", err)
		return err
	}

	mux := http.NewServeMux()
	// mutex and block profiles are empty unless sampling is on.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(10000)
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
		os.Remove(path)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("pprof server stopped", "err", err)
		}
	}()

	a.pprofPath = path
	slog.Info("pprof armed", "socket", path)
	return nil
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
		PprofSocket:       a.pprofPath,
	}
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
