package angelus_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
)

// Issue #59: profiling a hot daemon must not need a restart, which ends every
// live aria's turn. The RPC arms the socket, a heap profile is served over it,
// and disarming removes it.
func TestThePprofSocketArmsAndDisarmsWithoutARestart(t *testing.T) {
	t.Setenv("FIGARO_PPROF", "")
	a, cli, ctx := daemonFixture(t)
	_, err := os.Stat(a.PprofSocketPath())
	require.True(t, os.IsNotExist(err), "armed without being asked")

	sock, err := cli.ArmPprof(ctx, true)
	require.NoError(t, err)
	require.Equal(t, a.PprofSocketPath(), sock)
	fi, err := os.Stat(sock)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	again, err := cli.ArmPprof(ctx, true)
	require.NoError(t, err)
	require.Equal(t, sock, again, "arming twice is one socket")

	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := hc.Get("http://x/debug/pprof/heap?debug=1")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, sock, a.MemStatus().PprofSocket)

	off, err := cli.ArmPprof(ctx, false)
	require.NoError(t, err)
	require.Empty(t, off)
	_, err = os.Stat(sock)
	require.True(t, os.IsNotExist(err), "disarmed but the socket stayed")
	require.Empty(t, a.MemStatus().PprofSocket)
	require.Zero(t, runtime.SetMutexProfileFraction(-1), "disarmed but mutex profiling still samples")
}

// El Inspector X: bwrap binds the daemon's RuntimeDir read-write into every
// box at the owner's uid, so a pprof socket there answers inside a box, which
// can then profile the daemon, read its argv, or swap the socket for its own.
// The socket lives in a 0700 directory outside RuntimeDir.
func TestThePprofSocketIsOutsideTheRuntimeDirBoxesSee(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "run")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o700))

	for name, cfg := range map[string]angelus.Config{
		"default": {RuntimeDir: runtimeDir},
		"state":   {RuntimeDir: runtimeDir, PprofDir: filepath.Join(root, "state")},
	} {
		t.Run(name, func(t *testing.T) {
			a := angelus.New(cfg)
			sock, err := a.ArmPprof(true)
			require.NoError(t, err)
			defer a.ArmPprof(false)

			rel, err := filepath.Rel(runtimeDir, sock)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(rel, ".."), "pprof socket %s is under the runtime dir %s", sock, runtimeDir)
			fi, err := os.Stat(filepath.Dir(sock))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
			if cfg.PprofDir != "" {
				require.Equal(t, filepath.Join(cfg.PprofDir, angelus.PprofSocketName), sock)
			}
		})
	}
}

// El Inspector X: MkdirAll leaves an existing directory as it is, so the
// fallback beside the runtime dir must be checked, not assumed: a directory
// another user can enter, or a link to one, is refused rather than used.
func TestThePprofFallbackDirIsRefusedUnlessPrivate(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "run")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o700))
	fallback := runtimeDir + "-pprof"

	require.NoError(t, os.Mkdir(fallback, 0o755))
	require.NoError(t, os.Chmod(fallback, 0o755))
	_, err := angelus.New(angelus.Config{RuntimeDir: runtimeDir}).ArmPprof(true)
	require.ErrorContains(t, err, "mode 755")

	require.NoError(t, os.Remove(fallback))
	elsewhere := filepath.Join(root, "elsewhere")
	require.NoError(t, os.Mkdir(elsewhere, 0o700))
	require.NoError(t, os.Symlink(elsewhere, fallback))
	_, err = angelus.New(angelus.Config{RuntimeDir: runtimeDir}).ArmPprof(true)
	require.ErrorContains(t, err, "not a directory")

	require.NoError(t, os.Remove(fallback))
	require.NoError(t, os.Mkdir(fallback, 0o700))
	a := angelus.New(angelus.Config{RuntimeDir: runtimeDir})
	sock, err := a.ArmPprof(true)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fallback, angelus.PprofSocketName), sock)
	_, _ = a.ArmPprof(false)
}
