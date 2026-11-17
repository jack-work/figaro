package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func (u *daemonUnit) restarting() bool {
	return u.Sub == "auto-restart" || u.Sub == "auto-restart-queued"
}

func (u *daemonUnit) down() bool {
	return !u.restarting() && (u.Active == "inactive" || u.Active == "failed")
}

func unitRuntimeDir(u *daemonUnit, getenv func(string) string) string {
	env := map[string]string{}
	for _, kv := range u.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if d := env["FIGARO_RUNTIME_DIR"]; d != "" {
		return d
	}
	if d := env["XDG_RUNTIME_DIR"]; d != "" {
		return filepath.Join(d, "figaro")
	}
	if u.Scope == "user" {
		if d := getenv("XDG_RUNTIME_DIR"); d != "" {
			return filepath.Join(d, "figaro")
		}
	}
	return ""
}

func unitGoverns(u *daemonUnit, ours string, getenv func(string) string) bool {
	if u == nil {
		return false
	}
	d := unitRuntimeDir(u, getenv)
	return d != "" && filepath.Clean(d) == filepath.Clean(ours)
}

type unitWatch struct {
	show    func() *daemonUnit
	start   func(*daemonUnit) error
	answers func() bool
	say     io.Writer
	dial    time.Duration
	recheck time.Duration
	notice  time.Duration
	limit   time.Duration
}

func (w unitWatch) await(u *daemonUnit) error {
	began := time.Now()
	started, noticed := false, false
	lastShow := began
	for {
		if u.down() {
			if started {
				return fmt.Errorf("%s is %s and nothing answers the socket: journalctl %s",
					u.Name, u.Active, strings.TrimSpace(scopeFlag(u)+" -u "+u.Name))
			}
			if u.Scope != "user" {
				return fmt.Errorf("%s is %s and nothing answers the socket; figaro does not start a daemon outside the unit: sudo systemctl start %s",
					u.Name, u.Active, u.Name)
			}
			fmt.Fprintf(w.say, "angelus: %s is %s; starting it\n", u.Name, u.Active)
			if err := w.start(u); err != nil {
				return fmt.Errorf("systemctl --user start %s: %w", u.Name, err)
			}
			started, noticed = true, true
			lastShow = time.Now()
			if u = w.show(); u == nil {
				return fmt.Errorf("the unit vanished after systemctl --user start")
			}
			continue
		}
		if w.answers() {
			return nil
		}
		if time.Since(began) > w.limit {
			return fmt.Errorf("%s has not answered the socket in %s", u.Name, w.limit)
		}
		if !noticed && time.Since(began) > w.notice {
			noticed = true
			fmt.Fprintf(w.say, "angelus: waiting for %s (%s/%s), not starting a daemon outside it\n", u.Name, u.Active, u.Sub)
		}
		time.Sleep(w.dial)
		if time.Since(lastShow) >= w.recheck {
			lastShow = time.Now()
			cur := w.show()
			if cur == nil {
				return fmt.Errorf("%s is no longer loaded and nothing answers the socket", u.Name)
			}
			u = cur
		}
	}
}

func awaitGoverningUnit() bool {
	u := findDaemonUnit(daemonUnitName())
	if !unitGoverns(u, boot.AngelusRuntimeDir(), os.Getenv) {
		return false
	}
	ep := transport.UnixEndpoint(boot.AngelusSocketPath())
	w := unitWatch{
		show: func() *daemonUnit { return showDaemonUnit(u.Name, u.Scope) },
		start: func(u *daemonUnit) error {
			out, err := exec.Command("systemctl", "--user", "start", u.Name).CombinedOutput()
			if err != nil && len(out) > 0 {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return err
		},
		answers: func() bool {
			cli, err := sdk.DialAngelus(ep)
			if err != nil {
				return false
			}
			cli.Close()
			return true
		},
		say:     stderrw,
		dial:    50 * time.Millisecond,
		recheck: time.Second,
		notice:  startupNoticeAfter,
		limit:   startupHardCap,
	}
	if err := w.await(u); err != nil {
		die("%s", err)
	}
	cli, err := sdk.DialAngelus(ep)
	if err != nil {
		die("connect angelus: %s", err)
	}
	checkDaemonBuild(cli)
	cli.Close()
	return true
}
