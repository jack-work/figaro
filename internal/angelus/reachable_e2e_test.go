package angelus_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// systemd wipes /run/user/<uid> when the last session ends. A daemon that
// survives that must not be left serving nobody, invisible to `figaro stop`,
// while it still holds the store lock that keeps a replacement from starting.
func TestADaemonWhoseRuntimeDirIsWipedBecomesReachableAgain(t *testing.T) {
	a, _, _ := daemonFixture(t)
	pidPath := filepath.Join(a.RuntimeDir, "angelus.pid")
	require.FileExists(t, pidPath)

	require.NoError(t, os.RemoveAll(a.RuntimeDir))

	require.Eventually(t, func() bool {
		c, err := sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
		if err != nil {
			return false
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err = c.Status(ctx)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "the daemon never answered on its socket again")
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(pidPath)
		return err == nil && len(raw) > 0
	}, 10*time.Second, 100*time.Millisecond, "the pid file stop reads was never rewritten")
}
