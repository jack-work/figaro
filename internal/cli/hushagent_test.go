package cli

import (
	"github.com/jack-work/figaro/internal/boot"

	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/hush/managed"
	"github.com/stretchr/testify/require"
)

// fakeHushAgent speaks hush's agent wire, one JSON request and response per
// connection; the real agent is not importable.
type fakeHushAgent struct {
	sock string
	ln   net.Listener

	mu   sync.Mutex
	ops  []string
	done chan struct{}
}

func newFakeHushAgent(t *testing.T, dir string) *fakeHushAgent {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	sock := boot.HushAgentSocket(dir)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	a := &fakeHushAgent{sock: sock, ln: ln, done: make(chan struct{})}
	go a.serve()
	t.Cleanup(func() { ln.Close(); os.Remove(sock) })
	return a
}

func (a *fakeHushAgent) serve() {
	for {
		conn, err := a.ln.Accept()
		if err != nil {
			return
		}
		var req struct {
			Op string `json:"op"`
		}
		if err := json.NewDecoder(conn).Decode(&req); err == nil {
			a.mu.Lock()
			a.ops = append(a.ops, req.Op)
			a.mu.Unlock()
			_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true})
			if req.Op == "shutdown" {
				close(a.done)
			}
		}
		conn.Close()
	}
}

func (a *fakeHushAgent) received() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.ops...)
}

func isolateRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	require.Equal(t, dir, boot.AngelusRuntimeDir())
	return dir
}

func TestClaimHushAgentRecordsTheSocketItOwns(t *testing.T) {
	isolateRuntime(t)
	hushRun := filepath.Join(t.TempDir(), "run")

	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, hushRun))

	claimed, err := os.ReadFile(boot.HushClaimPath())
	require.NoError(t, err)
	require.Equal(t, boot.HushAgentSocket(hushRun)+"\n", string(claimed))
}

// An external hush is never figaro's to stop, so no claim is written.
func TestClaimHushAgentRefusesToClaimAnExternalHush(t *testing.T) {
	isolateRuntime(t)
	hushRun := filepath.Join(t.TempDir(), "run")

	require.NoError(t, boot.ClaimHushAgent(managed.ModeExternal, hushRun))

	_, err := os.Stat(boot.HushClaimPath())
	require.ErrorIs(t, err, os.ErrNotExist,
		"an external hush agent must never be claimed, and so never stopped")
}

func TestRetireHushAgentShutsDownTheClaimedAgent(t *testing.T) {
	isolateRuntime(t)
	agent := newFakeHushAgent(t, filepath.Join(t.TempDir(), "run"))
	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, filepath.Dir(agent.sock)))

	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped)
	require.Empty(t, heldBy)
	require.Equal(t, agent.sock, sock)

	select {
	case <-agent.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent was never asked to shut down")
	}
	require.Equal(t, []string{"shutdown"}, agent.received())

	// A stale claim naming a recycled socket path is how a stop reaches a stranger.
	_, err = os.Stat(boot.HushClaimPath())
	require.ErrorIs(t, err, os.ErrNotExist)
}

// Stop acts on the socket the daemon advertised, never one derived from the
// environment.
func TestRetireHushAgentTouchesOnlyTheClaimedSocket(t *testing.T) {
	isolateRuntime(t)
	mine := newFakeHushAgent(t, filepath.Join(t.TempDir(), "mine"))
	theirs := newFakeHushAgent(t, filepath.Join(t.TempDir(), "theirs"))
	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, filepath.Dir(mine.sock)))

	_, stopped, _, err := retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped)

	<-mine.done
	require.Empty(t, theirs.received(),
		"another figaro's agent was contacted; stop must act on the claim, not on the environment")
}

func TestRetireHushAgentWithoutAClaimDoesNothing(t *testing.T) {
	isolateRuntime(t)
	agent := newFakeHushAgent(t, filepath.Join(t.TempDir(), "run"))

	sock, stopped, _, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Empty(t, sock)
	require.Empty(t, agent.received())
}

// A claim on a socket nobody answers is cleared, or every later stop retries it.
func TestRetireHushAgentClearsAClaimOnADeadSocket(t *testing.T) {
	dir := isolateRuntime(t)
	dead := filepath.Join(t.TempDir(), "run")
	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, dead))

	sock, stopped, _, err := retireHushAgent()
	require.NoError(t, err, "a socket nobody answers is the outcome we wanted")
	require.False(t, stopped)
	require.Equal(t, boot.HushAgentSocket(dead), sock)

	_, err = os.Stat(filepath.Join(dir, "hush-agent"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func writeDaemonPID(t *testing.T, runtimeDir string, pid int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(runtimeDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(runtimeDir, "angelus.pid"),
		[]byte(fmt.Sprintf("%d\n", pid)), 0o600))
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

func claimAs(t *testing.T, runtimeDir, hushRuntimeDir string) {
	t.Helper()
	t.Setenv("FIGARO_RUNTIME_DIR", runtimeDir)
	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, hushRuntimeDir))
}

// Two daemons on one hush surface: stopping one leaves the agent to the other.
func TestRetireHushAgentLeavesAnAgentAnotherLiveDaemonIsUsing(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	theirRuntime := filepath.Join(t.TempDir(), "their-run")
	writeDaemonPID(t, theirRuntime, os.Getpid())
	claimAs(t, theirRuntime, hushRun)

	myRuntime := isolateRuntime(t)
	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, hushRun))

	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Equal(t, agent.sock, sock)
	require.Equal(t, []string{theirRuntime}, heldBy)
	require.Empty(t, agent.received(),
		"the other daemon's agent was shut down from under it")

	_, err = os.Stat(boot.HushClaimPath())
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(boot.HushUsersDir(sock), boot.HushUserKey(myRuntime)))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(boot.HushUsersDir(sock), boot.HushUserKey(theirRuntime)))
	require.NoError(t, err)
}

// A registration whose daemon died is pruned and does not hold the agent.
func TestRetireHushAgentPrunesADeadDaemonAndRetires(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	goneRuntime := filepath.Join(t.TempDir(), "gone-run")
	writeDaemonPID(t, goneRuntime, deadPID(t))
	claimAs(t, goneRuntime, hushRun)

	isolateRuntime(t)
	require.NoError(t, boot.ClaimHushAgent(managed.ModeEmbedded, hushRun))

	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped, "a registration whose daemon is dead must not hold the agent")
	require.Empty(t, heldBy)
	<-agent.done

	_, err = os.Stat(filepath.Join(boot.HushUsersDir(sock), boot.HushUserKey(goneRuntime)))
	require.ErrorIs(t, err, os.ErrNotExist, "the dead daemon's registration should have been pruned")
}

// A runtime dir outlives a reboot and the socket under /tmp does not, so a
// claim can name a path another daemon's agent now answers; the registry
// tells them apart.
func TestAStaleClaimCannotAdoptTheAgentOfALiveDaemon(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	liveRuntime := filepath.Join(t.TempDir(), "live-run")
	writeDaemonPID(t, liveRuntime, os.Getpid())
	claimAs(t, liveRuntime, hushRun)

	// The claim survived the reboot; the registration beside the socket did not.
	staleRuntime := isolateRuntime(t)
	require.NoError(t, os.WriteFile(filepath.Join(staleRuntime, "hush-agent"),
		[]byte(agent.sock+"\n"), 0o600))

	_, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Equal(t, []string{liveRuntime}, heldBy)
	require.Empty(t, agent.received(),
		"a claim from a previous boot adopted an agent that is not its own")
}

func asDaemon(t *testing.T, runtimeDir string) {
	t.Helper()
	t.Setenv("FIGARO_RUNTIME_DIR", runtimeDir)
	require.Equal(t, runtimeDir, boot.AngelusRuntimeDir())
}

// A shared agent lives until its last daemon stops; a crashed daemon does not pin it.
func TestASharedHushAgentLivesUntilItsLastDaemonStops(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	first := filepath.Join(t.TempDir(), "first-run")
	second := filepath.Join(t.TempDir(), "second-run")
	crashed := filepath.Join(t.TempDir(), "crashed-run")
	writeDaemonPID(t, first, os.Getpid())
	writeDaemonPID(t, second, os.Getpid())
	writeDaemonPID(t, crashed, deadPID(t))
	claimAs(t, first, hushRun)
	claimAs(t, second, hushRun)
	claimAs(t, crashed, hushRun)

	asDaemon(t, first)
	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Equal(t, []string{second}, heldBy,
		"only the live daemon should hold the agent; the crashed one must have been pruned")
	require.Empty(t, agent.received(), "the agent was shut down while a daemon was still using it")

	_, err = os.Stat(filepath.Join(boot.HushUsersDir(sock), boot.HushUserKey(crashed)))
	require.ErrorIs(t, err, os.ErrNotExist)

	asDaemon(t, second)
	sock, stopped, heldBy, err = retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped, "the last daemon out must retire the agent")
	require.Empty(t, heldBy)
	select {
	case <-agent.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent was never asked to shut down")
	}
	require.Equal(t, []string{"shutdown"}, agent.received())

	entries, err := os.ReadDir(boot.HushUsersDir(sock))
	require.NoError(t, err)
	require.Empty(t, entries, "the registry should be empty once every daemon has stopped")
}
