package boot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/jack-work/hush/managed"
)

// HushClaimPath names the file where a daemon advertises the socket of the
// embedded hush agent it owns. The embedded agent outlives its spawner and every
// figaro shares one argv, so stop must read the daemon's claim rather than derive
// a socket from its own shell environment; an external hush leaves no claim.
func HushClaimPath() string {
	return filepath.Join(AngelusRuntimeDir(), "hush-agent")
}

// HushUsersDir is the registry of daemons sharing one agent socket; stop retires
// the agent only when no other live daemon is registered. It lives beside the
// socket because that is the only path two daemons agree on (nix rewrites TMPDIR
// per shell).
func HushUsersDir(sock string) string {
	return filepath.Join(filepath.Dir(sock), "figaro-users")
}

func HushUserKey(runtimeDir string) string {
	sum := sha256.Sum256([]byte(runtimeDir))
	return hex.EncodeToString(sum[:8])
}

// ClaimHushAgent advertises the embedded agent this daemon owns and registers the
// daemon as one of its users.
func ClaimHushAgent(mode managed.Mode, hushRuntimeDir string) error {
	if mode != managed.ModeEmbedded {
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
	// The entry holds the runtime dir in the clear so stop can ask whether
	// this daemon is still alive.
	if err := os.WriteFile(filepath.Join(HushUsersDir(sock), HushUserKey(mine)), []byte(mine+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(HushClaimPath(), []byte(sock+"\n"), 0o600)
}

// HushAgentSocket mirrors managed.setupEmbedded: hush exposes the dir but not the
// socket path.
func HushAgentSocket(runtimeDir string) string {
	return filepath.Join(runtimeDir, "agent.sock")
}
