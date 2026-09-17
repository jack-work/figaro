package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jack-work/hush/client"
	"github.com/jack-work/hush/managed"
)

// The hush agent is the daemon's dependent, and nothing used to retire it.
//
// hush/managed spawns an EMBEDDED agent by re-exec'ing the consuming binary
// with HUSH_AGENT_CHILD=1, and its contract is that the agent OUTLIVES the
// process that spawned it: a sound default for hush, whose agent is shared
// across consumers and across upgrades. figaro's is not shared. It is scoped
// to figaro's own AppName, it is spawned by the daemon, it is kept alive by
// the daemon (keepHushAlive), and when the daemon is gone nothing will ever
// ask it anything again. It then sits on its socket holding an unlocked
// identity until its TTL, which figaro sets to 24 hours.
//
// So retiring it is figaro's policy, not hush's, and `figaro stop` is where
// it belongs.
//
// WHICH agent is the whole difficulty. Every figaro on the machine is the
// same executable with the same argv; a dev shell's agent, the user's real
// one and this one are distinguishable only by the socket they answer. And
// the socket a CLI would DERIVE from its own environment is not necessarily
// the one the daemon it is stopping actually has: FIGARO_HUSH_APP and
// FIGARO_HUSH_DIR are shell state, and a `figaro stop` typed in the wrong
// shell would then reach into another figaro's vault. So the daemon
// ADVERTISES the agent it owns, and stop reads that claim. The claim is a
// fact recorded by the only process that knows it, rather than a guess
// reconstructed by a process that does not.

// hushClaimPath is where a daemon advertises the embedded hush agent it
// owns: one line, the absolute path of that agent's socket. It sits beside
// angelus.pid, in the runtime dir, so it is scoped exactly the way the
// daemon is.
//
// Its EXISTENCE is the claim "this agent is mine to stop". A daemon using an
// external hush (the user's own `hush up`) writes nothing, and stop therefore
// has nothing to act on, which is the correct answer rather than a check
// somebody has to remember to write.
func hushClaimPath() string {
	return filepath.Join(angelusRuntimeDir(), "hush-agent")
}

// ONE AGENT CAN HAVE SEVERAL DAEMONS, and a claim alone cannot see them.
//
// The agent belongs to a hush SURFACE, not to a daemon. Three dev shells
// (`.#share-hush`, `.#snapshot`, `.#sandbox`) exist precisely to isolate the
// runtime dir while SHARING that surface, so a sandbox daemon and the user's
// real one both use /tmp/figaro-hush/agent.sock and each writes a claim
// naming it. A claim-only stop in the sandbox would then shut down the agent
// the live daemon is mid-turn against: keepHushAlive would rebuild it within
// five minutes, which is five minutes of "no credential" and exactly the
// failure keepHushAlive exists to prevent.
//
// The same shape answers the stale-claim question. A runtime dir on disk
// (`/var/tmp/...`, which a test or a dev root may well use) outlives a
// reboot while the agent socket under /tmp does not, so a claim can survive
// into a world where a DIFFERENT daemon has since started an agent at that
// same path. Adopting it would be the same misfire arriving by another road.
//
// So a daemon registers as a USER of the socket, beside the socket, and stop
// retires the agent only when no other live daemon is still registered.
//
// Beside the socket is the point: that directory is the only path two
// daemons are guaranteed to agree on, because they derive it from the socket
// they already share. A figaro-owned registry under os.TempDir() would not
// survive contact with nix, which rewrites TMPDIR per shell: that is the
// documented reason the shared-hush preset has to reset TMPDIR at all (see
// mkHushKnob in flake.nix), and a registry with the same flaw would have
// every shell believing it was alone.
func hushUsersDir(sock string) string {
	return filepath.Join(filepath.Dir(sock), "figaro-users")
}

// hushUserKey names this daemon's registry entry. The runtime dir is the
// daemon's identity here (it holds the socket, the pid file and the claim),
// and it is hashed because it is a path being used as a filename.
func hushUserKey(runtimeDir string) string {
	sum := sha256.Sum256([]byte(runtimeDir))
	return hex.EncodeToString(sum[:8])
}

// claimHushAgent records the socket of the embedded agent this daemon owns,
// and registers this daemon as one of that agent's users.
//
// It takes the mode and the runtime dir rather than a *managed.Hush so the
// external case is reachable from a test: constructing a managed.Hush in
// external mode requires a live external agent, which a test may not have,
// and the one branch that must never be wrong is exactly the one that would
// then go unexercised.
func claimHushAgent(mode managed.Mode, hushRuntimeDir string) error {
	if mode != managed.ModeEmbedded {
		// An external agent belongs to the user or to a service manager.
		// Killing it is not ours to do.
		return nil
	}
	if hushRuntimeDir == "" {
		return errors.New("hush claim: empty runtime dir")
	}
	sock := hushAgentSocket(hushRuntimeDir)
	mine := angelusRuntimeDir()
	if err := os.MkdirAll(mine, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(hushUsersDir(sock), 0o700); err != nil {
		return err
	}
	// The entry holds the runtime dir in the clear: that is what a later
	// stop needs in order to ask whether this daemon is still alive.
	if err := os.WriteFile(filepath.Join(hushUsersDir(sock), hushUserKey(mine)), []byte(mine+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(hushClaimPath(), []byte(sock+"\n"), 0o600)
}

// hushAgentSocket is where hush/managed puts an agent's socket for a given
// runtime dir. It mirrors managed.setupEmbedded, which joins "agent.sock"
// onto the resolved RuntimeDir; hush exposes the dir (via Config) but not
// the assembled path.
func hushAgentSocket(runtimeDir string) string {
	return filepath.Join(runtimeDir, "agent.sock")
}

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
	raw, readErr := os.ReadFile(hushClaimPath())
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return "", false, nil, nil
		}
		return "", false, nil, readErr
	}
	sock = strings.TrimSpace(string(raw))
	if sock == "" {
		os.Remove(hushClaimPath())
		return "", false, nil, nil
	}

	// Deregister first, so the question below is "is anyone ELSE using it".
	os.Remove(filepath.Join(hushUsersDir(sock), hushUserKey(angelusRuntimeDir())))
	// The claim is spent whatever the answer turns out to be.
	defer os.Remove(hushClaimPath())

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
	dir := hushUsersDir(sock)
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
