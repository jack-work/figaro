package angelus

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

// Angelus is the figaro supervisor.
type Angelus struct {
	Registry   *Registry
	Handlers   map[string]jkrpc.HandlerFunc // set before Run()
	Backend    store.Backend                // aria persistence (nil = ephemeral-only)
	SocketPath string
	RuntimeDir string
	StartedAt  time.Time

	listener net.Listener
	cancel   context.CancelFunc
}

// Config holds the settings for creating an Angelus.
type Config struct {
	RuntimeDir string        // e.g. $XDG_RUNTIME_DIR/figaro
	Backend    store.Backend // aria persistence (nil = ephemeral-only)
}

// New creates an Angelus. Call Run() to start it.
// Set a.Handlers before calling Run() to enable JSON-RPC.
//
// The backend (XwalBackend) owns each aria's shared log instance and
// closes it on Fork/Remove/Close, so there is no separate log cache:
// Open returns the same memoized instance to the live agent and to
// concurrent aria.read RPCs.
func New(cfg Config) *Angelus {
	a := &Angelus{
		Registry:   NewRegistry(),
		Backend:    cfg.Backend,
		SocketPath: filepath.Join(cfg.RuntimeDir, "angelus.sock"),
		RuntimeDir: cfg.RuntimeDir,
		StartedAt:  time.Now(), // set-once at construction; read concurrently (Uptime)
	}
	return a
}

// FigaroSocketDir returns the directory for figaro sockets.
func (a *Angelus) FigaroSocketDir() string {
	return filepath.Join(a.RuntimeDir, "figaros")
}

// BindingsPath returns the path for persisted PID bindings.
func (a *Angelus) BindingsPath() string {
	return filepath.Join(a.RuntimeDir, "bindings.json")
}

// Run starts the angelus and blocks until ctx is cancelled.
func (a *Angelus) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	ctx, span := figOtel.Start(ctx, "angelus.run",
		figOtel.WithAttributes(
			attribute.String("angelus.socket", a.SocketPath),
			attribute.Int("angelus.pid", os.Getpid()),
		),
	)
	defer span.End()

	if err := os.MkdirAll(a.FigaroSocketDir(), 0700); err != nil {
		return err
	}
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

	pidPath := filepath.Join(a.RuntimeDir, "angelus.pid")
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
	defer os.Remove(pidPath)

	// Remove socket on exit so `figaro rest` can detect shutdown.
	defer os.Remove(a.SocketPath)

	slog.Info("angelus started", "pid", os.Getpid(), "socket", a.SocketPath)

	go a.pidMonitor(ctx)
	go a.metaBackfill(ctx)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Info("angelus shutting down")
				return nil
			default:
				slog.Warn("angelus accept", "err", err)
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

	jconn := jkrpc.NewConn(conn)
	srv := jkrpc.NewServer(jconn, a.Handlers)

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
			slog.Info("pid died, unbinding", "pid", pid)
			a.Registry.Unbind(pid)
		}
	}
}

// Stop shuts down the angelus.
func (a *Angelus) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// Shutdown drains the registry and stops every figaro. Idempotent.
func (a *Angelus) Shutdown(perAgentGrace time.Duration) {
	if a.Registry == nil {
		a.Stop()
		return
	}
	if a.Registry.IsDraining() {
		return
	}
	a.Registry.SetDraining()

	figaros := a.Registry.All()
	slog.Info("angelus graceful shutdown beginning", "figaros", len(figaros))

	for _, f := range figaros {
		f.Interrupt()
	}

	var wg sync.WaitGroup
	for _, f := range figaros {
		wg.Add(1)
		go func(f figaro.Figaro) {
			defer wg.Done()
			waitForIdle(f, perAgentGrace)
			if err := a.Registry.Kill(f.ID()); err != nil {
				slog.Error("angelus kill", "id", f.ID(), "err", err)
			}
		}(f)
	}
	wg.Wait()

	slog.Info("angelus graceful shutdown complete")

	a.Stop()

	if a.Backend != nil {
		if err := a.Backend.Close(); err != nil {
			slog.Error("angelus backend close", "err", err)
		}
	}
}

// waitForIdle polls until State is "idle" or deadline.
func waitForIdle(f figaro.Figaro, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if f.Info().State == "idle" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
