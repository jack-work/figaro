package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

type stopOptions struct {
	force          bool
	keepAgent      bool
	forgetBindings bool
	hup            bool
}

func haltAndStop(cli *sdk.Angelus, o stopOptions, afterExit func()) {
	pid := daemonPID()
	if !o.forgetBindings {
		saveBindingsBeforeStop(cli)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	resp, err := cli.HaltAndStop(ctx)
	cancel()
	cli.Close()
	var answered *jkrpc.Error
	switch {
	case errors.As(err, &answered) && (answered.Code == methodNotFound || strings.Contains(answered.Message, "has no verb")):
		die("this angelus predates stop --hup: nothing was hung up and nothing was stopped; `figaro hup <aria>` hangs up one aria")
	case errors.As(err, &answered):
		die("the angelus refused to hang up its turns: %s\n  nothing was stopped", answered.Message)
	case err != nil:
		die("the angelus did not confirm the hang-up (%s)\n  if the request reached it, it hangs up every turn and stops itself; `figaro status` shows whether it is still up", err)
	}
	fmt.Fprintf(stderrw, "hung up %d running turn(s); none resumes after the restart\n", resp.Halted)
	if pid <= 0 {
		fmt.Fprintln(stderrw, "the angelus is stopping itself; its pid is unknown, so this does not wait for it")
		return
	}
	stopPid(pid, o.force, afterExit)
}

const methodNotFound = -32601

func daemonPID() int {
	if b, err := os.ReadFile(filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid")); err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(b), "%d", &pid); err == nil && pid > 0 {
			return pid
		}
	}
	if owner, held := boot.StoreLockOwner(); held && owner > 0 && pidAlive(owner) {
		return owner
	}
	return 0
}

func runStop(o stopOptions) {
	retireAgent := func() {
		if !o.keepAgent {
			reportRetiredHushAgent()
		}
	}
	afterExit := func() {
		retireAgent()
		if o.forgetBindings {
			forgetBindings()
		}
	}

	if d := resolveDoor(); d.configured {
		die("stop retires the daemon this shell started; %s is configured as the troupe, and figaro does not stop a daemon it reaches that way", d.label)
	}
	sockPath := boot.AngelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := sdk.DialAngelus(ep); err == nil {
		if o.hup {
			haltAndStop(cli, o, afterExit)
			return
		}
		if !o.forgetBindings {
			saveBindingsBeforeStop(cli)
		}
		cli.Close()
	} else if owner, held := boot.StoreLockOwner(); held && owner > 0 && pidAlive(owner) {
		if o.hup {
			die("angelus (pid %d) does not answer on %s, so its turns cannot be hung up; nothing was stopped", owner, sockPath)
		}
		fmt.Fprintf(stderrw, "angelus (pid %d) holds the store but does not answer on %s\n", owner, sockPath)
		stopPid(owner, o.force, afterExit)
		return
	} else {
		fmt.Fprintln(stderrw, "angelus is not running")
		afterExit()
		return
	}

	pidBytes, err := os.ReadFile(filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid"))
	if err != nil {
		if owner, held := boot.StoreLockOwner(); held && owner > 0 && pidAlive(owner) {
			stopPid(owner, o.force, afterExit)
			return
		}
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
	stopPid(pid, o.force, afterExit)
}

func saveBindingsBeforeStop(cli *sdk.Angelus) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := cli.SaveBindings(ctx)
	if err != nil {
		fmt.Fprintf(stderrw, "figaro: could not save pid bindings: %s\n", err)
		return
	}
	fmt.Fprintf(stderrw, "kept %d pid binding(s) for the next angelus\n", resp.Count)
}

func forgetBindings() {
	err := os.Remove(boot.BindingsPath(boot.AngelusRuntimeDir()))
	switch {
	case err == nil:
		fmt.Fprintln(stderrw, "pid bindings forgotten")
	case !os.IsNotExist(err):
		fmt.Fprintf(stderrw, "figaro: could not forget pid bindings: %s\n", err)
	}
}

func stopPid(pid int, force bool, afterExit func()) {
	sockPath := boot.AngelusSocketPath()

	if force {
		killPid(pid, syscall.SIGKILL)
		waitForExit(pid, 5*time.Second)
		os.Remove(sockPath)
		fmt.Fprintf(stderrw, "angelus (pid %d) forcefully terminated\n", pid)
		afterExit()
		return
	}

	killPid(pid, syscall.SIGTERM)

	if waitForExit(pid, 15*time.Second) {
		fmt.Fprintf(stderrw, "angelus (pid %d) put to rest\n", pid)
		afterExit()
		return
	}

	// keepHushAlive would respawn anything retired while the daemon is up.
	die("angelus (pid %d) did not rest within 15s; try `figaro rest --force`", pid)
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
