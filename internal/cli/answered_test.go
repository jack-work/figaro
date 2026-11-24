package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "c")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func withAnswerBounds(t *testing.T, command, answer time.Duration) {
	t.Helper()
	prevC, prevA := commandTimeout, answerBound
	commandTimeout, answerBound = command, answer
	t.Cleanup(func() { commandTimeout, answerBound = prevC, prevA })
}

func listenAt(t *testing.T, sock string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	return ln
}

func isolateColdStart(t *testing.T, runtimeDir string) *config.Loaded {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", runtimeDir)
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_TROUPE", "")
	t.Setenv("FIGARO_NO_BIND", "1")
	t.Setenv("FIGARO_NO_SELF_SPAWN", "1")
	loaded, err := config.Load(cfg)
	require.NoError(t, err)
	return loaded
}

func stderrOf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := stderrw
	stderrw = &buf
	t.Cleanup(func() { stderrw = prev })
	return &buf
}

// A socket-activated daemon: the socket takes connections at once, the
// daemon behind it starts accepting well after the command deadline.
func TestColdDaemonSlowerThanTheCommandDeadlineStillAnswersLs(t *testing.T) {
	backend, id := store.NewTestAria(t, "cold", message.Patch{})
	_, liveDir := serveBackend(t, backend)
	live := filepath.Join(liveDir, "angelus.sock")

	front := shortRuntimeDir(t)
	ln := listenAt(t, filepath.Join(front, "angelus.sock"))
	wake := time.Second
	go func() {
		time.Sleep(wake)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				d, err := net.Dial("unix", live)
				if err != nil {
					return
				}
				defer d.Close()
				go io.Copy(d, c)
				io.Copy(c, d)
			}()
		}
	}()

	withAnswerBounds(t, 200*time.Millisecond, 10*time.Second)
	loaded := isolateColdStart(t, front)

	start := time.Now()
	out := stdoutOf(t, func() { runList(loaded, lsOpts{jsonOut: true}) })
	require.GreaterOrEqual(t, time.Since(start), wake, "the daemon answered before it woke: the test proves nothing")
	var rows []rpc.FigaroInfoResponse
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	require.Contains(t, ids, id)
}

func TestDeadSocketFailsFastAndSaysNothingListens(t *testing.T) {
	sock := filepath.Join(shortRuntimeDir(t), "angelus.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	start := time.Now()
	_, _, err = dialAnswered(transport.UnixEndpoint(sock))
	require.Less(t, time.Since(start), time.Second)
	require.True(t, errors.Is(err, errNotListening), "%v", err)
	require.Contains(t, err.Error(), sock)
}

func TestDaemonThatHangsUpBeforeAnsweringDiesWithTheReason(t *testing.T) {
	front := shortRuntimeDir(t)
	ln := listenAt(t, filepath.Join(front, "angelus.sock"))
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	isolateColdStart(t, front)
	errs := stderrOf(t)

	start := time.Now()
	code, exited := captureExit(t, ensureAngelus)
	require.True(t, exited)
	require.Equal(t, 1, code)
	require.Less(t, time.Since(start), time.Second)
	require.Contains(t, errs.String(), "hung up before answering")
}

func TestDaemonThatNeverAnswersIsBoundedByTheAnswerBound(t *testing.T) {
	front := shortRuntimeDir(t)
	sock := filepath.Join(front, "angelus.sock")
	listenAt(t, sock)
	isolateColdStart(t, front)
	withAnswerBounds(t, 100*time.Millisecond, 300*time.Millisecond)

	start := time.Now()
	_, _, err := dialAnswered(transport.UnixEndpoint(sock))
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
	require.False(t, errors.Is(err, errNotListening))
	require.True(t, strings.Contains(err.Error(), "has not answered in 300ms"), "%v", err)
}
