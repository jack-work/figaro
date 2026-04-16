package angelus_test

import (
	"context"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
)

func newTestAngelus(t *testing.T) (*angelus.Angelus, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	logger := log.New(os.Stderr, "test-angelus: ", log.LstdFlags)
	a := angelus.New(angelus.Config{
		RuntimeDir: dir,
		Logger:     logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.Run(ctx)
	}()

	// Wait for socket to appear.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(a.SocketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("angelus.Run returned error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("angelus.Run did not return after cancel")
		}
	})

	return a, cancel
}

func TestAngelus_SocketCreated(t *testing.T) {
	a, _ := newTestAngelus(t)

	_, err := os.Stat(a.SocketPath)
	require.NoError(t, err, "angelus socket should exist")

	// Should be connectable.
	conn, err := net.Dial("unix", a.SocketPath)
	require.NoError(t, err)
	conn.Close()
}

func TestAngelus_FigaroSocketDir(t *testing.T) {
	a, _ := newTestAngelus(t)

	dir := a.FigaroSocketDir()
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestAngelus_RegistryAvailable(t *testing.T) {
	a, _ := newTestAngelus(t)

	// Registry should be usable.
	m := newMock("test-figaro")
	require.NoError(t, a.Registry.Register(m))
	assert.Equal(t, 1, a.Registry.FigaroCount())
}

func TestAngelus_PIDMonitorUnbindsDeadPID(t *testing.T) {
	a, _ := newTestAngelus(t)

	m := newMock("abc")
	require.NoError(t, a.Registry.Register(m))

	// Get a guaranteed-dead PID: start a process and let it exit.
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	deadPID := cmd.Process.Pid
	require.NoError(t, cmd.Wait()) // now dead

	require.NoError(t, a.Registry.Bind(deadPID, "abc"))

	// Wait for the monitor to reap it (polls every 2s).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.Registry.BoundPIDCount() == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, 0, a.Registry.BoundPIDCount(), "dead PID should have been unbound")
}

func TestAngelus_StartedAt(t *testing.T) {
	a, _ := newTestAngelus(t)
	assert.False(t, a.StartedAt.IsZero())
}

func TestAngelus_StaleSocketCleanup(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "angelus.sock")

	// Create a stale socket file.
	require.NoError(t, os.WriteFile(sockPath, []byte("stale"), 0600))

	logger := log.New(os.Stderr, "test-angelus: ", log.LstdFlags)
	a := angelus.New(angelus.Config{
		RuntimeDir: dir,
		Logger:     logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.Run(ctx)
	}()

	// Wait for socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Error("timeout")
	}
}

func TestAngelus_ShutdownDrainsAllFigaros(t *testing.T) {
	a, _ := newTestAngelus(t)

	// Register a few mock figaros.
	for _, id := range []string{"a1", "a2", "a3"} {
		require.NoError(t, a.Registry.Register(newMock(id)))
	}
	require.Equal(t, 3, a.Registry.FigaroCount())

	a.Shutdown(500 * time.Millisecond)

	assert.Zero(t, a.Registry.FigaroCount(), "all figaros should be killed")
	assert.True(t, a.Registry.IsDraining(), "registry should remain draining")
}

func TestAngelus_ShutdownRefusesNewRegistrations(t *testing.T) {
	a, _ := newTestAngelus(t)

	a.Shutdown(100 * time.Millisecond)

	err := a.Registry.Register(newMock("late"))
	require.Error(t, err, "Register should fail after shutdown")
	assert.Contains(t, err.Error(), "shutting down")
}

func TestAngelus_ShutdownIdempotent(t *testing.T) {
	a, _ := newTestAngelus(t)
	require.NoError(t, a.Registry.Register(newMock("only")))

	a.Shutdown(100 * time.Millisecond)
	// Second call must not panic, must not error, must do nothing harmful.
	a.Shutdown(100 * time.Millisecond)

	assert.Zero(t, a.Registry.FigaroCount())
}
