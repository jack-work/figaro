package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/transport"
)

func TestWaitForSocketWaitsPastStalePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "figaro.sock")
	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o600))

	listenerResult := make(chan struct {
		listenerCloser interface{ Close() error }
		err            error
	}, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		err := os.Remove(path)
		if err != nil {
			listenerResult <- struct {
				listenerCloser interface{ Close() error }
				err            error
			}{err: err}
			return
		}
		listener, err := transport.Listen(transport.UnixEndpoint(path))
		listenerResult <- struct {
			listenerCloser interface{ Close() error }
			err            error
		}{listenerCloser: listener, err: err}
	}()

	require.NoError(t, waitForSocket(path, time.Second))
	result := <-listenerResult
	require.NoError(t, result.err)
	t.Cleanup(func() { _ = result.listenerCloser.Close() })
}
