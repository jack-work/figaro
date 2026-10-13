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

// retireHushAgent shuts down the claimed agent and drops the claim. Call it
// only once the daemon is confirmed gone: keepHushAlive respawns the agent.
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

	// Deregister first, so the question below is whether anyone else uses it.
	os.Remove(filepath.Join(boot.HushUsersDir(sock), boot.HushUserKey(boot.AngelusRuntimeDir())))
	defer os.Remove(boot.HushClaimPath())

	heldBy, err = otherLiveHushUsers(sock)
	if err != nil {
		// Not provably alone: a leaked agent costs memory, a stolen one a turn.
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

// otherLiveHushUsers prunes dead registrations. A recycled pid reads as
// alive, which is the safe direction: the agent lingers rather than being
// taken from a daemon using it.
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

// hushAgentAlreadyGone: hush/managed keeps the same judgement unexported.
func hushAgentAlreadyGone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// reportRetiredHushAgent is silent when there was no claim.
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
