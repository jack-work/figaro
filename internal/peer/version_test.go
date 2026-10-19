package peer_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
)

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.buf.String(), s)
}

func captureLogs(t *testing.T) *logBuffer {
	t.Helper()
	lb := &logBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lb, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return lb
}

func staticAt(t *testing.T, version uint64, peers ...config.Peer) *peer.Static {
	t.Helper()
	m, err := peer.NewStatic(version, peers, nil)
	require.NoError(t, err)
	return m
}

// Each side of the node-door handshake learns the other's membership
// version. A difference is logged once, not on every link.
func TestTheHandshakeComparesMembershipVersions(t *testing.T) {
	logs := captureLogs(t)
	p := peertest.NewPKI(t, "fed")
	atB, atA := &peer.Versions{}, &peer.Versions{}
	b := serve(t, p.Door("b", ""), &peer.Door{Members: staticAt(t, 7, p.Peer("b", "a", "127.0.0.1:1")), Versions: atB})
	a := &peer.Dialer{Members: staticAt(t, 5, p.Peer("a", "b", b.addr)), Versions: atA}

	for range 3 {
		l, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
		require.NoError(t, err)
		require.NoError(t, <-b.admitted)
		l.Close()
	}

	got, ok := atB.Seen("a")
	require.True(t, ok)
	require.Equal(t, uint64(5), got.Version)
	got, ok = atA.Seen("b")
	require.True(t, ok)
	require.Equal(t, uint64(7), got.Version)
	require.Equal(t, 2, logs.count("membership version differs"), "one line per side, not one per link")
}

// Agreeing versions are recorded and log nothing.
func TestAgreeingVersionsLogNothing(t *testing.T) {
	logs := captureLogs(t)
	p := peertest.NewPKI(t, "fed")
	atB, atA := &peer.Versions{}, &peer.Versions{}
	b := serve(t, p.Door("b", ""), &peer.Door{Members: staticAt(t, 4, p.Peer("b", "a", "127.0.0.1:1")), Versions: atB})
	a := &peer.Dialer{Members: staticAt(t, 4, p.Peer("a", "b", b.addr)), Versions: atA}

	l, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	require.NoError(t, <-b.admitted)
	l.Close()

	got, ok := atB.Seen("a")
	require.True(t, ok)
	require.Equal(t, uint64(4), got.Version)
	require.Zero(t, logs.count("membership version differs"))
}
