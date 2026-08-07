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

// PprofEnv arms the profiler when set to a non-empty value.
//
// Off by default, and deliberately so: pprof's handlers are an
// unauthenticated remote-execution-adjacent surface (a profile request
// stops the world; /debug/pprof/cmdline leaks the daemon's argv), and the
// daemon is long-lived. Behind a unix socket with 0600 in the runtime dir
// it is reachable only by the owning user, which is the same trust
// boundary as angelus.sock itself.
const PprofEnv = "FIGARO_PPROF"

// PprofSocketName is the socket's name inside the runtime dir.
const PprofSocketName = "pprof.sock"

// PprofSocketPath is where the profiler listens when armed.
func (a *Angelus) PprofSocketPath() string {
	return filepath.Join(a.RuntimeDir, PprofSocketName)
}

// StartPprof serves net/http/pprof on a unix socket if FIGARO_PPROF is
// set. Returns nil (armed nothing) otherwise.
//
// Attach with the socket, not a port:
//
//	go tool pprof -http=: 'http+unix://$XDG_RUNTIME_DIR/figaro/pprof.sock/debug/pprof/heap'
//
// or, if your Go is too old for http+unix:
//
//	curl --unix-socket $XDG_RUNTIME_DIR/figaro/pprof.sock \
//	     http://x/debug/pprof/heap > heap.out && go tool pprof heap.out
func (a *Angelus) StartPprof(ctx context.Context) error {
	if os.Getenv(PprofEnv) == "" {
		return nil
	}
	path := a.PprofSocketPath()
	os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		// Profiling is diagnostic: never fatal to the daemon.
		slog.Warn("pprof socket unavailable", "path", path, "err", err)
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		slog.Warn("pprof socket chmod failed", "path", path, "err", err)
		return err
	}

	mux := http.NewServeMux()
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
// it. ReadMemStats stops the world briefly, so this is a status call —
// not something to put on a ticker.
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
	return st
}

// UnlimitedMemLimit is what debug.SetMemoryLimit reports when no soft
// ceiling is armed.
const UnlimitedMemLimit int64 = math.MaxInt64
