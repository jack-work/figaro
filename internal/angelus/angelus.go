package angelus

import (
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	// NOTE: golang.org/x/sys/unix is Linux/macOS only. For future Windows
	// support, PID monitoring will need a build-tagged alternative using
	// golang.org/x/sys/windows or os.FindProcess + signal probing.
	"golang.org/x/sys/unix"
)

// Angelus is the figaro supervisor. It owns the registry, listens on
// a unix socket for JSON-RPC requests, and monitors bound PIDs.
type Angelus struct {
	Registry   *Registry
	SocketPath string
	RuntimeDir string
	Logger     *log.Logger
	StartedAt  time.Time

	listener net.Listener
	cancel   context.CancelFunc
}

// Config holds the settings for creating an Angelus.
type Config struct {
	RuntimeDir string // e.g. $XDG_RUNTIME_DIR/figaro
	Logger     *log.Logger
}

// New creates an Angelus. Call Run() to start it.
func New(cfg Config) *Angelus {
	return &Angelus{
		Registry:   NewRegistry(),
		SocketPath: filepath.Join(cfg.RuntimeDir, "angelus.sock"),
		RuntimeDir: cfg.RuntimeDir,
		Logger:     cfg.Logger,
	}
}

// FigaroSocketDir returns the directory for figaro sockets.
func (a *Angelus) FigaroSocketDir() string {
	return filepath.Join(a.RuntimeDir, "figaros")
}

// Run starts the angelus: creates directories, listens on the socket,
// starts the PID monitor, and blocks until ctx is cancelled.
func (a *Angelus) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.StartedAt = time.Now()

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

	a.Logger.Printf("angelus started, socket=%s", a.SocketPath)

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

// handleConn is a placeholder for the jrpc2 handler (Step 5 wires this).
// For now it closes the connection.
func (a *Angelus) handleConn(ctx context.Context, conn net.Conn) {
	// TODO: wire jrpc2 server here in Step 5.
	conn.Close()
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
