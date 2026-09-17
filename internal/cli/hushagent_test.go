package cli

import (
	"encoding/json"
	"net"
	"os"
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

	sock, stopped, err := retireHushAgent()
	require.NoError(t, err)
	require.True(t, stopped)
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

	_, stopped, err := retireHushAgent()
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

	sock, stopped, err := retireHushAgent()
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

	sock, stopped, err := retireHushAgent()
	require.NoError(t, err, "a socket nobody answers is the outcome we wanted")
	require.False(t, stopped)
	require.Equal(t, hushAgentSocket(dead), sock)

	_, err = os.Stat(filepath.Join(dir, "hush-agent"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
