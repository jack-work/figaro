package angelus

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/jsonrpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"

	// NOTE: golang.org/x/sys/unix is Linux/macOS only. For future Windows
	// support, PID monitoring will need a build-tagged alternative using
	// golang.org/x/sys/windows or os.FindProcess + signal probing.
	"golang.org/x/sys/unix"
)

// Angelus is the figaro supervisor. It owns the registry, listens on
// a unix socket for JSON-RPC requests, and monitors bound PIDs.
type Angelus struct {
	Registry   *Registry
	Handlers   map[string]jsonrpc.HandlerFunc // set before Run()
	Backend    store.Backend                  // aria persistence (nil = ephemeral-only)
	SocketPath string
	RuntimeDir string
	Logger     *log.Logger
	StartedAt  time.Time

	listener net.Listener
	cancel   context.CancelFunc
}

// Config holds the settings for creating an Angelus.
type Config struct {
	RuntimeDir string        // e.g. $XDG_RUNTIME_DIR/figaro
	Logger     *log.Logger
	Backend    store.Backend // aria persistence (nil = ephemeral-only)
}

// New creates an Angelus. Call Run() to start it.
// Set a.Handlers before calling Run() to enable JSON-RPC.
func New(cfg Config) *Angelus {
	return &Angelus{
		Registry:   NewRegistry(),
		Backend:    cfg.Backend,
		SocketPath: filepath.Join(cfg.RuntimeDir, "angelus.sock"),
		RuntimeDir: cfg.RuntimeDir,
		Logger:     cfg.Logger,
	}
}

// FigaroSocketDir returns the directory for figaro sockets.
func (a *Angelus) FigaroSocketDir() string {
	return filepath.Join(a.RuntimeDir, "figaros")
}

// BindingsPath returns the path for persisted PID bindings.
// Consumed on startup after RestoreArias; written by SaveBindings
// when the user asks for `figaro rest --keep-pids`.
func (a *Angelus) BindingsPath() string {
	return filepath.Join(a.RuntimeDir, "bindings.json")
}

// Run starts the angelus: creates directories, listens on the socket,
// starts the PID monitor, and blocks until ctx is cancelled.
func (a *Angelus) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.StartedAt = time.Now()

	ctx, span := figOtel.Start(ctx, "angelus.run",
		figOtel.WithAttributes(
			attribute.String("angelus.socket", a.SocketPath),
			attribute.Int("angelus.pid", os.Getpid()),
		),
	)
	defer span.End()

	// Create runtime directories.
	if err := os.MkdirAll(a.FigaroSocketDir(), 0700); err != nil {
		return err
	}

	// Clean stale socket.
	os.Remove(a.SocketPath)

	ln, err := net.Listen("unix", a.SocketPath)
	if err != nil {
		return err
	}
	a.listener = ln

	if err := os.Chmod(a.SocketPath, 0600); err != nil {
		ln.Close()
		return err
	}

	// Write PID file for clean shutdown.
	pidPath := filepath.Join(a.RuntimeDir, "angelus.pid")
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
	defer os.Remove(pidPath)

	// Remove the socket file on exit so `figaro rest` can detect that
	// the angelus has finished shutting down (it polls for the absence
	// of this file).
	defer os.Remove(a.SocketPath)

	a.Logger.Printf("angelus started, pid=%d, socket=%s", os.Getpid(), a.SocketPath)

	// Start PID monitor.
	go a.pidMonitor(ctx)

	// Accept loop.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				a.Logger.Printf("angelus shutting down")
				return nil
			default:
				a.Logger.Printf("accept error: %v", err)
				continue
			}
		}
		go a.handleConn(ctx, conn)
	}
}

// handleConn serves a single JSON-RPC connection.
func (a *Angelus) handleConn(ctx context.Context, conn net.Conn) {
	if a.Handlers == nil {
		conn.Close()
		return
	}

	jconn := jsonrpc.NewConn(conn)
	srv := jsonrpc.NewServer(jconn, a.Handlers)

	done := make(chan struct{})
	go func() {
		srv.Serve(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		srv.Stop()
	}
}

// pidMonitor polls bound PIDs every 2 seconds and unbinds dead ones.
func (a *Angelus) pidMonitor(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reapDeadPIDs()
		}
	}
}

// reapDeadPIDs checks all bound PIDs and unbinds any that are no longer alive.
func (a *Angelus) reapDeadPIDs() {
	pids := a.Registry.AllPIDs()
	for _, pid := range pids {
		if !isAlive(pid) {
			a.Logger.Printf("pid %d died, unbinding", pid)
			a.Registry.Unbind(pid)
		}
	}
}

// isAlive checks if a process is running using kill(pid, 0).
func isAlive(pid int) bool {
	return unix.Kill(pid, 0) == nil
}

// Stop shuts down the angelus.
func (a *Angelus) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// Shutdown drains the registry and stops every figaro gracefully.
// It runs in three phases:
//
//  1. Mark the registry as draining (rejects new figaro.create).
//  2. For each figaro: send Interrupt(), poll its Info().State for
//     up to perAgentGrace, then call Kill() which flushes the store.
//  3. Cancel the angelus context so the accept loop exits.
//
// Idempotent. Safe to call from a signal handler. Returns when every
// figaro has been killed (or its grace expired).
func (a *Angelus) Shutdown(perAgentGrace time.Duration) {
	if a.Registry == nil {
		a.Stop()
		return
	}
	if a.Registry.IsDraining() {
		return // already shutting down
	}
	a.Registry.SetDraining()

	figaros := a.Registry.All()
	if a.Logger != nil {
		a.Logger.Printf("angelus: graceful shutdown beginning, %d figaro(s) to drain", len(figaros))
	}

	// Phase 1: ask everyone to interrupt their current turn.
	for _, f := range figaros {
		f.Interrupt()
	}

	// Phase 2: wait for each one to reach idle, then kill it. We do
	// this in parallel — one slow figaro shouldn't starve the others.
	var wg sync.WaitGroup
	for _, f := range figaros {
		wg.Add(1)
		go func(f figaro.Figaro) {
			defer wg.Done()
			waitForIdle(f, perAgentGrace)
			if err := a.Registry.Kill(f.ID()); err != nil && a.Logger != nil {
				a.Logger.Printf("angelus: kill %s: %v", f.ID(), err)
			}
		}(f)
	}
	wg.Wait()

	if a.Logger != nil {
		a.Logger.Printf("angelus: graceful shutdown complete")
	}

	// Phase 3: stop the accept loop.
	a.Stop()

	// Phase 4: close the backend. Safe now that every agent has
	// flushed its Downstream handle in phase 2. No-op for FileBackend.
	if a.Backend != nil {
		if err := a.Backend.Close(); err != nil && a.Logger != nil {
			a.Logger.Printf("angelus: backend close: %v", err)
		}
	}
}

// waitForIdle polls the figaro's State, returning early as soon as
// it reads "idle" or after the grace deadline.
func waitForIdle(f figaro.Figaro, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if f.Info().State == "idle" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
