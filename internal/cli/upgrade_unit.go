//go:build !windows

package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/jack-work/figaro/internal/boot"
)

func sameExecutable(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

func unitMayServe(u *daemonUnit, to string) error {
	if u.Scope != "user" {
		return fmt.Errorf("%s is a system unit, so figaro upgrade does not restart it: point its ExecStart at %s, then run sudo systemctl restart %s",
			u.Name, to, u.Name)
	}
	if sameExecutable(u.ExecPath, to) {
		return nil
	}
	reload := ""
	if u.NeedReload {
		reload = " (its unit file changed on disk: run systemctl --user daemon-reload, then upgrade again)"
	}
	return fmt.Errorf("%s runs %s, not %s: its ExecStart decides which build serves, so a restart would bring the same build back. Point ExecStart at %s, then upgrade again%s",
		u.Name, orUnknown(u.ExecPath), to, to, reload)
}

func servingMismatch(u *daemonUnit, owner int, to string, exeOf func(int) string) error {
	if owner <= 0 {
		return fmt.Errorf("cannot tell which process answers %s, so the new build is unconfirmed", boot.AngelusSocketPath())
	}
	if owner != u.MainPID {
		return fmt.Errorf("pid %d answers the socket, not %s (pid %d): a daemon outside the unit serves", owner, u.Name, u.MainPID)
	}
	if exe := exeOf(owner); !sameExecutable(exe, to) {
		return fmt.Errorf("%s (pid %d) runs %s, not %s", u.Name, owner, orUnknown(exe), to)
	}
	return nil
}

func procExe(pid int) string {
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return exe
}

func restartUnitServing(u *daemonUnit, to string) {
	cur := showDaemonUnit(u.Name, u.Scope)
	if cur == nil {
		die("%s is no longer loaded; nothing serves the socket", u.Name)
	}
	if err := liveUnitWatch(cur, true).await(cur); err != nil {
		die("%s", err)
	}
	owner, err := socketOwnerPID(boot.AngelusSocketPath())
	if err != nil {
		owner = 0
	}
	if cur = showDaemonUnit(u.Name, u.Scope); cur == nil {
		die("%s is no longer loaded", u.Name)
	}
	if err := servingMismatch(cur, owner, to, procExe); err != nil {
		die("upgrade not confirmed: %s", err)
	}
}
