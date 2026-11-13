package angelus_test

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
)

// An endpoint peer that sends one huge frame is cut off at the cap instead of
// being buffered whole, and the daemon keeps serving everyone else.
func TestTheEndpointCutsOffAFrameOverTheCap(t *testing.T) {
	angelus.SetMaxEndpointFrameForTest(t, 4<<10)
	a, acli, ctx := daemonFixture(t)

	nc, err := net.Dial("unix", a.SocketPath)
	require.NoError(t, err)
	defer nc.Close()
	frame := `{"jsonrpc":"2.0","id":1,"method":"angelus.list","params":{"x":"` + strings.Repeat("A", 64<<10) + `"}}` + "\n"
	_, _ = nc.Write([]byte(frame))
	require.NoError(t, nc.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1024)
	n, err := nc.Read(buf)
	require.Error(t, err, "the daemon answered a frame over the cap: %q", buf[:n])
	require.False(t, strings.Contains(err.Error(), "timeout"), "the connection was left open: %v", err)

	_, err = acli.List(ctx)
	require.NoError(t, err, "one peer's oversize frame took the door down for others")
}
