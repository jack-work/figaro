package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

func haltAndStop(cli *sdk.Angelus, pid int, o stopOptions, afterExit func()) {
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
		die("this angelus predates stop --hup: nothing was hung up and nothing was stopped; `figaro hup <figaro>` hangs up one figaro")
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

func readPidFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err != nil || pid <= 0 {
		return 0, fmt.Errorf("unreadable pid in %s", path)
	}
	return pid, nil
}

func liveDaemonPID() (int, error) {
	pidPath := filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid")
	pid, pidErr := readPidFile(pidPath)
	if pidErr == nil && pidAlive(pid) {
		return pid, nil
	}
	if owner, held := boot.StoreLockOwner(); held && owner > 0 && pidAlive(owner) {
		return owner, nil
	}
	if errors.Is(pidErr, fs.ErrPermission) {
		return 0, pidErr
	}
	return 0, nil
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
	pidPath := filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid")
	pid, permErr := liveDaemonPID()
	if permErr != nil {
		die("cannot read %s (%s); stop a system angelus through its service manager", pidPath, permErr)
	}
	if pid == 0 {
		if known, listening := socketListening(sockPath); known {
			if listening {
				fmt.Fprintf(stderrw, "angelus is not running; %s is held open by its service manager, which starts it on the next call\n", sockPath)
			} else {
				removeUnheldSocket(sockPath)
				fmt.Fprintln(stderrw, "angelus is not running")
			}
			afterExit()
			return
		}
		ep := transport.UnixEndpoint(sockPath)
		cli, err := sdk.DialAngelus(ep)
		if err != nil {
			removeUnheldSocket(sockPath)
			fmt.Fprintln(stderrw, "angelus is not running")
			afterExit()
			return
		}
		if storeHasNoDaemon() {
			cli.Close()
			fmt.Fprintf(stderrw, "angelus is not running; %s is held open by its service manager, which starts it on the next call\n", sockPath)
			afterExit()
			return
		}
		if o.hup {
			haltAndStop(cli, 0, o, afterExit)
			return
		}
		if !o.forgetBindings {
			saveBindingsBeforeStop(cli)
		}
		cli.Close()
		fmt.Fprintln(stderrw, "angelus pid file missing; nothing was stopped")
		return
	}

	ep := transport.UnixEndpoint(sockPath)
	if cli, err := sdk.DialAngelus(ep); err == nil {
		if o.hup {
			haltAndStop(cli, pid, o, afterExit)
			return
		}
		if !o.forgetBindings {
			saveBindingsBeforeStop(cli)
		}
		cli.Close()
	} else {
		if o.hup {
			die("angelus (pid %d) does not answer on %s, so its turns cannot be hung up; nothing was stopped", pid, sockPath)
		}
		fmt.Fprintf(stderrw, "angelus (pid %d) holds the store but does not answer on %s\n", pid, sockPath)
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
		removeUnheldSocket(sockPath)
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

	die("angelus (pid %d) did not rest within 15s; try `figaro rest --force`", pid)
}

func storeHasNoDaemon() bool {
	f, err := os.OpenFile(boot.StoreLockPath(), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	f.Close()
	_, held := boot.StoreLockOwner()
	return !held
}

func removeUnheldSocket(path string) {
	if known, listening := socketListening(path); known {
		if !listening {
			_ = os.Remove(path)
		}
		return
	}
	if cli, err := sdk.DialAngelus(transport.UnixEndpoint(path)); err == nil {
		cli.Close()
		return
	}
	_ = os.Remove(path)
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
