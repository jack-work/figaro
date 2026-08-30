package angelus

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/store"
)

func TestMemStatusReportsCounters(t *testing.T) {
	a := New(Config{RuntimeDir: t.TempDir()})
	a.Sessions.Create("aria-a", "sleep 30")
	t.Cleanup(func() { a.Sessions.KillScope("aria-a") })

	st := a.MemStatus()
	require.NotNil(t, st)
	require.Positive(t, st.Goroutines)
	require.Positive(t, st.HeapAllocBytes, "heap numbers unread: %+v", st)
	require.Positive(t, st.SysBytes)
	require.Positive(t, st.MemLimitBytes, "MaxInt64 when unlimited, never zero")
	require.Equal(t, 1, st.Sessions)
	require.Zero(t, st.LiveArias)
	require.Empty(t, st.PprofSocket, "advertises a profiler never armed")
}

// Off by default: pprof's handlers stop the world and leak argv, and this
// daemon is long-lived. Armed, it must be reachable and owner-only.
func TestStartPprof(t *testing.T) {
	t.Run("unarmed", func(t *testing.T) {
		t.Setenv(PprofEnv, "")
		a := New(Config{RuntimeDir: t.TempDir()})
		require.NoError(t, a.StartPprof(context.Background()))
		_, err := os.Stat(a.PprofSocketPath())
		require.True(t, os.IsNotExist(err), "socket exists without %s", PprofEnv)
	})

	t.Run("armed", func(t *testing.T) {
		t.Setenv(PprofEnv, "1")
		a := New(Config{RuntimeDir: t.TempDir()})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, a.StartPprof(ctx))

		sock := a.PprofSocketPath()
		fi, err := os.Stat(sock)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), fi.Mode().Perm())
		require.Equal(t, sock, a.MemStatus().PprofSocket)

		c := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		}}
		resp, err := c.Get("http://x/debug/pprof/heap?debug=1")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	})
}

// The two fields that explain RSS must actually be reported, and a forced
// collection must report a live set, not another unswept number.
func TestMemCollectReportsTheLiveSetAndTheOSFacing(t *testing.T) {
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}

	plain := a.MemStatus()
	require.Nil(t, plain.Collected, "an ordinary status must not stop the world")
	require.NotZero(t, plain.HeapSysBytes)
	// HeapIdle >= HeapReleased always: released is the part of idle handed
	// back. A build reporting the reverse has them crossed.
	require.GreaterOrEqual(t, plain.HeapIdleBytes, plain.HeapReleasedBytes)

	// Make garbage that nothing holds, then prove a collection sees it.
	before := a.MemStatus().HeapAllocBytes
	junk := make([][]byte, 0, 256)
	for i := 0; i < 256; i++ {
		junk = append(junk, make([]byte, 64*1024)) // 16MB, all reachable
	}
	require.Greater(t, a.MemStatus().HeapAllocBytes, before)
	junk = nil
	_ = junk

	got := a.MemCollectStatus()
	require.NotNil(t, got.Collected, "--gc must report what it collected")
	require.Equal(t, got.HeapAllocBytes, got.Collected.AfterBytes,
		"the reported heap must be the post-collection one")
	require.Greater(t, got.Collected.BeforeBytes, got.Collected.AfterBytes,
		"16MB of unreachable garbage survived a forced collection")
	require.GreaterOrEqual(t, got.Collected.ReclaimedByes, uint64(8<<20),
		"reclaimed should account for most of the garbage")
	require.NotZero(t, got.Collected.Took)
}
