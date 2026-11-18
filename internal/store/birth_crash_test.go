package store

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	birthCrashDir    = "FIGARO_BIRTH_CRASH_DIR"
	birthCrashParent = "FIGARO_BIRTH_CRASH_PARENT"
	birthCrashExit   = 3
)

// The fork is acknowledged while its fsync is stalled, and then the daemon
// dies without closing the store. Reopened, the fork is there with its birth
// patch: an acknowledged birth is in its segments before the ack, so only a
// machine stop inside the stall can lose it.
func TestAnAckedBirthSurvivesADaemonCrashInsideItsSync(t *testing.T) {
	if dir := os.Getenv(birthCrashDir); dir != "" {
		bornThenDie(dir, os.Getenv(birthCrashParent))
		return
	}
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	require.NoError(t, err)
	parent := mustNewAriaTB(t, be)
	writeBoard(t, be, parent, 0, 3)
	require.NoError(t, be.Close())

	cmd := exec.Command(os.Args[0], "-test.run=^TestAnAckedBirthSurvivesADaemonCrashInsideItsSync$")
	cmd.Env = append(os.Environ(), birthCrashDir+"="+dir, birthCrashParent+"="+parent)
	out, err := cmd.Output()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "the child must die, not return: %s", out)
	require.Equal(t, birthCrashExit, exit.ExitCode(), "child: %s %s", out, exit.Stderr)
	child := strings.TrimSpace(strings.TrimPrefix(string(out), "BORN "))
	require.NotEmpty(t, child)

	be, err = NewXwalBackend(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })
	_, ok := be.store.Node(child)
	require.True(t, ok, "the acknowledged fork vanished")
	snap, err := be.FormState(child)
	require.NoError(t, err)
	raw, ok := snap.Get("birth.mark")
	require.True(t, ok, "the acknowledged fork came back without its birth patch")
	var mark string
	require.NoError(t, json.Unmarshal(raw, &mark))
	require.Equal(t, "crash", mark)
}

func bornThenDie(dir, parent string) {
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		os.Exit(5)
	}
	stalled := make(chan struct{})
	be.store.syncs.before = func() { <-stalled }
	child, _, _, err := be.store.ForkWith(parent, 0, birthPatch("crash"))
	if err != nil {
		os.Exit(6)
	}
	os.Stdout.WriteString("BORN " + child + "\n")
	os.Exit(birthCrashExit)
}
