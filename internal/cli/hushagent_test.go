package cli

import (
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

// fakeHushAgent is a unix socket speaking hush's agent wire: one JSON
// request in, one JSON response out, per connection. It records the ops it
// was asked to perform.
//
// The real agent is not importable (hush/internal/agent), and spawning one
// costs an age identity and a process, which is what the gated e2e in
// hush_stop_e2e_test.go is for. What these tests need to see is figaro's
// DECISION: which socket it acts on, and which it leaves alone.
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
	sock := hushAgentSocket(dir)
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

// isolateRuntime points angelusRuntimeDir at a scratch dir for one test, so
// the claim file can never be the user's.
func isolateRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	require.Equal(t, dir, angelusRuntimeDir())
	return dir
}

func TestClaimHushAgentRecordsTheSocketItOwns(t *testing.T) {
	isolateRuntime(t)
	hushRun := filepath.Join(t.TempDir(), "run")

	require.NoError(t, claimHushAgent(managed.ModeEmbedded, hushRun))

	claimed, err := os.ReadFile(hushClaimPath())
	require.NoError(t, err)
	require.Equal(t, hushAgentSocket(hushRun)+"\n", string(claimed))
}

// The claim is a promise that the agent is figaro's to stop. An external
// hush belongs to the user or to a service manager, so there must be no
// promise to act on.
//
// This is the branch that cannot be reached through a real *managed.Hush in
// a test (external mode needs a live external agent), and it is the one
// branch whose failure would reach into somebody else's vault. Hence the
// mode is a parameter.
func TestClaimHushAgentRefusesToClaimAnExternalHush(t *testing.T) {
	isolateRuntime(t)
	hushRun := filepath.Join(t.TempDir(), "run")

	require.NoError(t, claimHushAgent(managed.ModeExternal, hushRun))

	_, err := os.Stat(hushClaimPath())
	require.ErrorIs(t, err, os.ErrNotExist,
		"an external hush agent must never be claimed, and so never stopped")
}

func TestRetireHushAgentShutsDownTheClaimedAgent(t *testing.T) {
	isolateRuntime(t)
	agent := newFakeHushAgent(t, filepath.Join(t.TempDir(), "run"))
	require.NoError(t, claimHushAgent(managed.ModeEmbedded, filepath.Dir(agent.sock)))

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

	// The claim is spent: a second stop must not try again, and a stale
	// claim naming a recycled socket path is how this reaches a stranger.
	_, err = os.Stat(hushClaimPath())
	require.ErrorIs(t, err, os.ErrNotExist)
}

// The invariant that makes the whole thing safe, stated as a test rather
// than as a comment: figaro acts on the socket the daemon ADVERTISED, never
// on one it derives from its own environment. Two figaros, two agents, one
// claim; the unclaimed agent must hear nothing.
func TestRetireHushAgentTouchesOnlyTheClaimedSocket(t *testing.T) {
	isolateRuntime(t)
	mine := newFakeHushAgent(t, filepath.Join(t.TempDir(), "mine"))
	theirs := newFakeHushAgent(t, filepath.Join(t.TempDir(), "theirs"))
	require.NoError(t, claimHushAgent(managed.ModeEmbedded, filepath.Dir(mine.sock)))

	_, stopped, _, err := retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped)

	<-mine.done
	require.Empty(t, theirs.received(),
		"another figaro's agent was contacted; stop must act on the claim, not on the environment")
}

func TestRetireHushAgentWithoutAClaimDoesNothing(t *testing.T) {
	isolateRuntime(t)
	// An agent IS running. Nobody claimed it, so it is not ours.
	agent := newFakeHushAgent(t, filepath.Join(t.TempDir(), "run"))

	sock, stopped, _, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Empty(t, sock)
	require.Empty(t, agent.received())
}

// A daemon that was killed rather than stopped leaves a claim naming a
// socket nobody answers. That is the ordinary case, not an error, and it
// must clear the claim: otherwise every later stop retries a corpse.
func TestRetireHushAgentClearsAClaimOnADeadSocket(t *testing.T) {
	dir := isolateRuntime(t)
	dead := filepath.Join(t.TempDir(), "run")
	require.NoError(t, claimHushAgent(managed.ModeEmbedded, dead))

	sock, stopped, _, err := retireHushAgent()
	require.NoError(t, err, "a socket nobody answers is the outcome we wanted")
	require.False(t, stopped)
	require.Equal(t, hushAgentSocket(dead), sock)

	_, err = os.Stat(filepath.Join(dir, "hush-agent"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// writeDaemonPID plants an angelus.pid in a runtime dir, which is how one
// figaro decides whether another is still alive.
func writeDaemonPID(t *testing.T, runtimeDir string, pid int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(runtimeDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(runtimeDir, "angelus.pid"),
		[]byte(fmt.Sprintf("%d\n", pid)), 0o600))
}

// deadPID returns a pid that has certainly exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

// claimAs registers another daemon, at its own runtime dir, against sock.
func claimAs(t *testing.T, runtimeDir, hushRuntimeDir string) {
	t.Helper()
	t.Setenv("FIGARO_RUNTIME_DIR", runtimeDir)
	require.NoError(t, claimHushAgent(managed.ModeEmbedded, hushRuntimeDir))
}

// One agent, two daemons. `.#share-hush`, `.#snapshot` and `.#sandbox` all
// isolate the runtime dir while SHARING the hush surface, so this is the
// ordinary arrangement on a dev machine, not a corner. Stopping the sandbox
// must not take the agent out from under the live daemon: it would be
// mid-turn, and keepHushAlive would not rebuild it for up to five minutes.
func TestRetireHushAgentLeavesAnAgentAnotherLiveDaemonIsUsing(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	theirRuntime := filepath.Join(t.TempDir(), "their-run")
	writeDaemonPID(t, theirRuntime, os.Getpid()) // alive: this test process
	claimAs(t, theirRuntime, hushRun)

	myRuntime := isolateRuntime(t)
	require.NoError(t, claimHushAgent(managed.ModeEmbedded, hushRun))

	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Equal(t, agent.sock, sock)
	require.Equal(t, []string{theirRuntime}, heldBy)
	require.Empty(t, agent.received(),
		"the other daemon's agent was shut down from under it")

	// My claim and my registration are gone; theirs is untouched.
	_, err = os.Stat(hushClaimPath())
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(hushUsersDir(sock), hushUserKey(myRuntime)))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(hushUsersDir(sock), hushUserKey(theirRuntime)))
	require.NoError(t, err)
}

// The same registry, the other way: a daemon that registered and then died
// must not keep an agent alive forever. Its entry is pruned and the agent
// goes.
func TestRetireHushAgentPrunesADeadDaemonAndRetires(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	goneRuntime := filepath.Join(t.TempDir(), "gone-run")
	writeDaemonPID(t, goneRuntime, deadPID(t))
	claimAs(t, goneRuntime, hushRun)

	isolateRuntime(t)
	require.NoError(t, claimHushAgent(managed.ModeEmbedded, hushRun))

	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped, "a registration whose daemon is dead must not hold the agent")
	require.Empty(t, heldBy)
	<-agent.done

	_, err = os.Stat(filepath.Join(hushUsersDir(sock), hushUserKey(goneRuntime)))
	require.ErrorIs(t, err, os.ErrNotExist, "the dead daemon's registration should have been pruned")
}

// The stale-claim question, stated as a test. A runtime dir on disk outlives
// a reboot; the agent socket under /tmp does not. So a claim can survive into
// a world where a DIFFERENT daemon has since started an agent at that same
// path, and the claim alone cannot tell the difference. The registry can:
// this stop never registered against the socket that is there now.
func TestAStaleClaimCannotAdoptTheAgentOfALiveDaemon(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	// The daemon that owns the agent as things stand.
	liveRuntime := filepath.Join(t.TempDir(), "live-run")
	writeDaemonPID(t, liveRuntime, os.Getpid())
	claimAs(t, liveRuntime, hushRun)

	// A claim left behind by a daemon from a previous boot: the file is
	// there, the registration is not, because the registry lived beside
	// the socket and went with it.
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

// asDaemon makes the rest of the test speak as the daemon living at
// runtimeDir: the same switch `figaro stop` gets from its environment.
func asDaemon(t *testing.T, runtimeDir string) {
	t.Helper()
	t.Setenv("FIGARO_RUNTIME_DIR", runtimeDir)
	require.Equal(t, runtimeDir, angelusRuntimeDir())
}

// The rule, in one sequence: a shared agent lives until its LAST daemon
// stops, and a daemon that died without stopping does not pin it there.
//
// The two tests above take one half each, and the gated e2e drives the whole
// thing through real processes. This states it as a single in-suite fact, so
// a change that gets one step right and another wrong cannot pass by halves.
func TestASharedHushAgentLivesUntilItsLastDaemonStops(t *testing.T) {
	hushRun := filepath.Join(t.TempDir(), "run")
	agent := newFakeHushAgent(t, hushRun)

	// Three daemons on one hush surface. Two are running; the third died
	// without ever stopping, which is what a SIGKILL or a reboot leaves.
	first := filepath.Join(t.TempDir(), "first-run")
	second := filepath.Join(t.TempDir(), "second-run")
	crashed := filepath.Join(t.TempDir(), "crashed-run")
	writeDaemonPID(t, first, os.Getpid())
	writeDaemonPID(t, second, os.Getpid())
	writeDaemonPID(t, crashed, deadPID(t))
	claimAs(t, first, hushRun)
	claimAs(t, second, hushRun)
	claimAs(t, crashed, hushRun)

	// FIRST STOP: the second daemon is still using the agent, so it stays,
	// and stop says whose it is.
	asDaemon(t, first)
	sock, stopped, heldBy, err := retireHushAgent()
	require.NoError(t, err)
	require.False(t, stopped)
	require.Equal(t, []string{second}, heldBy,
		"only the live daemon should hold the agent; the crashed one must have been pruned")
	require.Empty(t, agent.received(), "the agent was shut down while a daemon was still using it")

	// The crashed daemon's registration is gone on the way past, so it can
	// never pin the agent: that is what makes the last stop able to act.
	_, err = os.Stat(filepath.Join(hushUsersDir(sock), hushUserKey(crashed)))
	require.ErrorIs(t, err, os.ErrNotExist)

	// SECOND STOP: nobody is left, so the agent goes.
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

	// Nothing of any of the three is left behind.
	entries, err := os.ReadDir(hushUsersDir(sock))
	require.NoError(t, err)
	require.Empty(t, entries, "the registry should be empty once every daemon has stopped")
}
