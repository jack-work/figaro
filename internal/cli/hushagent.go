package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/boot"
)

// retireHushAgent shuts down the agent the daemon claimed, and drops the
// claim. It reports the socket it acted on, whether an agent was actually
// there to stop, and which other live daemons held it back.
//
// Call it only once the daemon is CONFIRMED GONE. keepHushAlive respawns the
// agent on a ticker, so retiring it under a live daemon buys a few seconds of
// nothing.
//
// A claim naming a socket nobody answers is the ordinary case after a daemon
// was killed rather than stopped: hush reports that as ENOENT or
// ECONNREFUSED, and both mean the outcome we wanted. Anything else is
// reported, because a vault that refuses to shut down is worth a line on
// stderr.
func retireHushAgent() (sock string, stopped bool, heldBy []string, err error) {
	raw, readErr := os.ReadFile(boot.HushClaimPath())
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return "", false, nil, nil
		}
		return "", false, nil, readErr
	}
	sock = strings.TrimSpace(string(raw))
	if sock == "" {
		os.Remove(boot.HushClaimPath())
		return "", false, nil, nil
	}

	// Deregister first, so the question below is "is anyone ELSE using it".
	os.Remove(filepath.Join(boot.HushUsersDir(sock), boot.HushUserKey(boot.AngelusRuntimeDir())))
	// The claim is spent whatever the answer turns out to be.
	defer os.Remove(boot.HushClaimPath())

	heldBy, err = otherLiveHushUsers(sock)
	if err != nil {
		// We could not establish that we are alone, so we do not act.
		// A leaked agent costs memory; a stolen one costs somebody's turn.
		return sock, false, nil, err
	}
	if len(heldBy) > 0 {
		return sock, false, heldBy, nil
	}

	shutErr := client.NewWithSocket(sock).Shutdown()
	switch {
	case shutErr == nil:
		return sock, true, nil, nil
	case hushAgentAlreadyGone(shutErr):
		return sock, false, nil, nil
	default:
		return sock, false, nil, shutErr
	}
}

// otherLiveHushUsers reports the runtime dirs of daemons still registered
// against sock and still running, pruning the entries of those that are not.
//
// Liveness is the registered daemon's own angelus.pid, which is the same
// witness `figaro stop` already trusts for the daemon in front of it. A pid
// that has been recycled onto an unrelated process reads as ALIVE, and that
// is the direction to be wrong in: the agent lingers until the next stop
// instead of being taken out from under a daemon that is using it.
func otherLiveHushUsers(sock string) ([]string, error) {
	dir := boot.HushUsersDir(sock)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var live []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		runtimeDir := strings.TrimSpace(string(raw))
		if runtimeDir == "" || !daemonAliveAt(runtimeDir) {
			os.Remove(path)
			continue
		}
		live = append(live, runtimeDir)
	}
	return live, nil
}

// daemonAliveAt reports whether a daemon is running out of runtimeDir,
// judged by the pid file it writes there.
func daemonAliveAt(runtimeDir string) bool {
	raw, err := os.ReadFile(filepath.Join(runtimeDir, "angelus.pid"))
	if err != nil {
		return false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil || pid <= 0 {
		return false
	}
	return pidAlive(pid)
}

// hushAgentAlreadyGone reports whether a shutdown failed because there was
// nothing left to shut down. managed keeps its own copy of this judgement
// unexported, so figaro states it here: a missing socket file, or one no
// process is listening on.
func hushAgentAlreadyGone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// reportRetiredHushAgent runs the retirement and says what happened, in the
// same voice as the rest of `figaro stop`. Silence when there was no claim:
// most users have never heard of the agent and do not need to.
func reportRetiredHushAgent() {
	sock, stopped, heldBy, err := retireHushAgent()
	switch {
	case err != nil:
		fmt.Fprintf(stderrw, "hush agent at %s did not shut down: %s\n", sock, err)
	case len(heldBy) > 0:
		fmt.Fprintf(stderrw, "hush agent left running: still in use by %s\n", strings.Join(heldBy, ", "))
	case stopped:
		fmt.Fprintln(stderrw, "hush agent retired")
	}
}
