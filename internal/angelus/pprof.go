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

	"github.com/jack-work/figaro/internal/rpc"
)

// PprofEnv arms the profiler when set. Off by default because pprof's
// handlers are a real surface on a long-lived daemon, a profile request
// stops the world, /debug/pprof/cmdline leaks argv, and behind a 0600
// unix socket the reach is the owning user, same as angelus.sock.
const PprofEnv = "FIGARO_PPROF"

const PprofSocketName = "pprof.sock"

// UnlimitedMemLimit is what debug.SetMemoryLimit reports with no ceiling.
const UnlimitedMemLimit int64 = math.MaxInt64

func (a *Angelus) PprofSocketPath() string {
	return filepath.Join(a.RuntimeDir, PprofSocketName)
}

// StartPprof serves net/http/pprof on a unix socket when PprofEnv is set,
// and does nothing otherwise. Attach with:
//
//	go tool pprof -http=: 'http+unix://$XDG_RUNTIME_DIR/figaro/pprof.sock/debug/pprof/heap'
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
	// /debug/pprof/mutex and /block are served by Index but return nothing
	// unless sampling is on, and nothing turned it on. For a daemon whose
	// writers are now serialization points, contention is the profile worth
	// having.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(10000) // ns: sample blocking over 10us
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

// MemStatus reports the daemon's footprint and the counters that explain
// it. ReadMemStats stops the world, so this belongs on a status call and
// not on a ticker.
func (a *Angelus) MemStatus() *rpc.MemStatus {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	st := &rpc.MemStatus{
		LiveArias:      a.Registry.FigaroCount(),
		Goroutines:     runtime.NumGoroutine(),
		HeapAllocBytes: ms.HeapAlloc,
		HeapInuseBytes: ms.HeapInuse,
		HeapSysBytes:   ms.HeapSys,
		SysBytes:       ms.Sys,
		NumGC:          ms.NumGC,
		MemLimitBytes:  debug.SetMemoryLimit(-1), // -1 reads without setting
		PprofSocket:    a.pprofPath,
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
