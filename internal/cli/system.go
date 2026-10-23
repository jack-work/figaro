package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jack-work/figaro/api/transport"
)

func runRestWithFlags(force, keepPIDs bool) {

	// --keep-pids says a restart is coming: the hush agent holds an unlocked
	// identity the next daemon would otherwise have to rebuild. Every other
	// path retires it.
	retireAgent := func() {
		if keepPIDs {
			return
		}
		reportRetiredHushAgent()
	}

	if d := resolveDoor(); d.configured {
		die("stop retires the daemon this shell started; %s is configured as the troupe, and figaro does not stop a daemon it reaches that way", d.label)
	}
	sockPath := boot.AngelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if keepPIDs {
		cli, err := sdk.DialAngelus(ep)
		if err != nil {
			fmt.Fprintln(stderrw, "angelus is not running")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := cli.SaveBindings(ctx)
		cancel()
		cli.Close()
		if err != nil {
			die("save-bindings: %s", err)
		}
		fmt.Fprintf(stderrw, "persisted %d pid binding(s)\n", resp.Count)
	} else if cli, err := sdk.DialAngelus(ep); err == nil {
		cli.Close()
	} else {
		// An agent outlives a daemon that was killed rather than stopped; the
		// claim file is the daemon's record of the one it owned.
		fmt.Fprintln(stderrw, "angelus is not running")
		retireAgent()
		return
	}

	pidBytes, err := os.ReadFile(filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid"))
	if err != nil {
		os.Remove(sockPath)
		fmt.Fprintln(stderrw, "angelus pid file missing; socket removed")
		return
	}
	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		os.Remove(sockPath)
		fmt.Fprintln(stderrw, "angelus pid file unreadable; socket removed")
		return
	}

	if force {
		killPid(pid, syscall.SIGKILL)
		waitForExit(pid, 5*time.Second)
		os.Remove(sockPath)
		fmt.Fprintf(stderrw, "angelus (pid %d) forcefully terminated\n", pid)
		// A SIGKILLed daemon runs no cleanup of its own.
		retireAgent()
		return
	}

	killPid(pid, syscall.SIGTERM)

	if waitForExit(pid, 15*time.Second) {
		fmt.Fprintf(stderrw, "angelus (pid %d) put to rest\n", pid)
		retireAgent()
		return
	}

	// keepHushAlive would respawn anything retired while the daemon is up.
	fmt.Fprintf(stderrw,
		"angelus (pid %d) did not rest within 15s; try `figaro rest --force`\n", pid)
}

func waitForExit(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if !pidAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
