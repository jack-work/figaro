package boot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/jack-work/hush/managed"
)

// hush/managed spawns an EMBEDDED agent by re-exec'ing the consuming binary,
// and its contract is that the agent OUTLIVES the process that spawned it.
// figaro's agent is scoped to figaro's own AppName, spawned and kept alive by
// the daemon, and useless once the daemon is gone, so retiring it is
// figaro's policy and `figaro stop` is where it belongs.
//
// WHICH agent is the whole difficulty. Every figaro on the machine is the
// same executable with the same argv; they are distinguishable only by the
// socket they answer. And the socket a CLI would DERIVE from its own
// environment is not necessarily the one the daemon it is stopping has:
// FIGARO_HUSH_APP and FIGARO_HUSH_DIR are shell state, and a `figaro stop`
// typed in the wrong shell would then reach into another figaro's vault. So
// the daemon ADVERTISES the agent it owns, and stop reads that claim.

// HushClaimPath is where a daemon advertises the embedded hush agent it
// owns: one line, the absolute path of that agent's socket. It sits beside
// angelus.pid, so it is scoped exactly the way the daemon is.
//
// Its EXISTENCE is the claim "this agent is mine to stop". A daemon using an
// external hush (the user's own `hush up`) writes nothing, and stop therefore
// has nothing to act on.
func HushClaimPath() string {
	return filepath.Join(AngelusRuntimeDir(), "hush-agent")
}

// ONE AGENT CAN HAVE SEVERAL DAEMONS, and a claim alone cannot see them.
//
// The agent belongs to a hush SURFACE, not to a daemon. The dev shells
// isolate the runtime dir while SHARING that surface, so a sandbox daemon
// and the user's real one both use the same agent.sock and each writes a
// claim naming it. A claim-only stop in the sandbox would shut down the
// agent the live daemon is mid-turn against.
//
// The same shape answers the stale-claim question: a runtime dir on disk
// outlives a reboot while the agent socket under /tmp does not, so a claim
// can survive into a world where a DIFFERENT daemon has since started an
// agent at that same path.
//
// So a daemon registers as a USER of the socket, beside the socket, and stop
// retires the agent only when no other live daemon is still registered.
// Beside the socket is the point: that directory is the only path two
// daemons are guaranteed to agree on. A registry under os.TempDir() would
// not survive nix, which rewrites TMPDIR per shell.
func HushUsersDir(sock string) string {
	return filepath.Join(filepath.Dir(sock), "figaro-users")
}

// HushUserKey names a daemon's registry entry: its runtime dir, hashed
// because it is a path being used as a filename.
func HushUserKey(runtimeDir string) string {
	sum := sha256.Sum256([]byte(runtimeDir))
	return hex.EncodeToString(sum[:8])
}

// ClaimHushAgent records the socket of the embedded agent this daemon owns,
// and registers this daemon as one of that agent's users.
//
// It takes the mode and the runtime dir rather than a *managed.Hush so the
// external case is reachable from a test: constructing a managed.Hush in
// external mode requires a live external agent.
func ClaimHushAgent(mode managed.Mode, hushRuntimeDir string) error {
	if mode != managed.ModeEmbedded {
		// An external agent belongs to the user or to a service manager.
		return nil
	}
	if hushRuntimeDir == "" {
		return errors.New("hush claim: empty runtime dir")
	}
	sock := HushAgentSocket(hushRuntimeDir)
	mine := AngelusRuntimeDir()
	if err := os.MkdirAll(mine, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(HushUsersDir(sock), 0o700); err != nil {
		return err
	}
	// The entry holds the runtime dir in the clear: that is what a later
	// stop needs in order to ask whether this daemon is still alive.
	if err := os.WriteFile(filepath.Join(HushUsersDir(sock), HushUserKey(mine)), []byte(mine+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(HushClaimPath(), []byte(sock+"\n"), 0o600)
}

// HushAgentSocket mirrors managed.setupEmbedded, which joins "agent.sock"
// onto the resolved RuntimeDir; hush exposes the dir but not the path.
func HushAgentSocket(runtimeDir string) string {
	return filepath.Join(runtimeDir, "agent.sock")
}
