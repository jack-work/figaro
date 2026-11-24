package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/figwal/xwal"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func (u *daemonUnit) restarting() bool {
	switch u.Sub {
	case "auto-restart", "auto-restart-queued", "dead-before-auto-restart", "failed-before-auto-restart":
		return true
	}
	return false
}

func (u *daemonUnit) down() bool {
	return !u.restarting() && (u.Active == "inactive" || u.Active == "failed")
}

var unitRuntimeKeys = []string{"FIGARO_RUNTIME_DIR", "XDG_RUNTIME_DIR"}

type unitEnvSources struct {
	manager  func(scope string) []string
	readFile func(string) ([]byte, error)
	getenv   func(string) string
}

func liveUnitEnvSources() unitEnvSources {
	return unitEnvSources{manager: managerEnvironment, readFile: os.ReadFile, getenv: os.Getenv}
}

func managerEnvironment(scope string) []string {
	args := []string{"show-environment"}
	if scope == "user" {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func keepRuntimeKeys(env map[string]string, assignments []string) {
	for _, kv := range assignments {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, want := range unitRuntimeKeys {
			if k == want {
				env[k] = v
			}
		}
	}
}

func environmentFileAssignments(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out = append(out, strings.TrimSpace(k)+"="+v)
	}
	return out
}

func unitRuntimeDir(u *daemonUnit, src unitEnvSources) string {
	env := map[string]string{}
	keepRuntimeKeys(env, src.manager(u.Scope))
	keepRuntimeKeys(env, u.Env)
	for _, f := range u.EnvFiles {
		if data, err := src.readFile(f); err == nil {
			keepRuntimeKeys(env, environmentFileAssignments(data))
		}
	}
	for _, k := range u.Unset {
		delete(env, k)
	}
	if d := env["FIGARO_RUNTIME_DIR"]; d != "" {
		return d
	}
	if d := env["XDG_RUNTIME_DIR"]; d != "" {
		return filepath.Join(d, "figaro")
	}
	if u.Scope == "user" {
		if d := src.getenv("XDG_RUNTIME_DIR"); d != "" {
			return filepath.Join(d, "figaro")
		}
	}
	return ""
}

func unitGoverns(u *daemonUnit, ours string, src unitEnvSources) bool {
	if u == nil {
		return false
	}
	d := unitRuntimeDir(u, src)
	return d != "" && filepath.Clean(d) == filepath.Clean(ours)
}

func governingUnit() *daemonUnit {
	u := findDaemonUnit(daemonUnitName())
	if !unitGoverns(u, boot.AngelusRuntimeDir(), liveUnitEnvSources()) {
		return nil
	}
	return u
}

type unitWatch struct {
	show     func() *daemonUnit
	start    func(*daemonUnit) error
	answers  func() bool
	working  func(pid int) bool
	mayStart bool
	sock     string
	say      io.Writer
	dial     time.Duration
	recheck  time.Duration
	notice   time.Duration
	stall    time.Duration
	limit    time.Duration
}

func journalHint(u *daemonUnit) string {
	return "journalctl " + strings.TrimSpace(scopeFlag(u)+" -u "+u.Name)
}

func (w unitWatch) await(u *daemonUnit) error {
	began := time.Now()
	started, noticed := false, false
	lastShow := began
	var seenPID int
	var seenAt time.Time
	for {
		if u.down() {
			if started {
				return fmt.Errorf("%s is %s and nothing answers the socket: %s", u.Name, u.Active, journalHint(u))
			}
			if u.Scope != "user" {
				return fmt.Errorf("%s is %s and nothing answers the socket; figaro does not start a daemon outside the unit: sudo systemctl start %s",
					u.Name, u.Active, u.Name)
			}
			if !w.mayStart {
				return fmt.Errorf("%s is %s and nothing answers the socket; FIGARO_NO_SELF_SPAWN is set, so figaro starts nothing, the unit included: systemctl --user start %s",
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
		now := time.Now()
		if u.MainPID != seenPID {
			seenPID, seenAt = u.MainPID, now
		}
		if seenPID > 0 && now.Sub(seenAt) > w.stall && !w.working(seenPID) {
			return fmt.Errorf("%s runs pid %d, which has not answered %s in %s: %s",
				u.Name, seenPID, w.sock, w.stall, journalHint(u))
		}
		if now.Sub(began) > w.limit {
			return fmt.Errorf("%s has not answered the socket in %s: %s", u.Name, w.limit, journalHint(u))
		}
		if !noticed && now.Sub(began) > w.notice {
			noticed = true
			fmt.Fprintf(w.say, "angelus: waiting for %s (%s/%s, pid %d), not starting a daemon outside it; %s\n",
				u.Name, u.Active, u.Sub, u.MainPID, journalHint(u))
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

const unitBindStall = 30 * time.Second

func daemonAtWork(pid int) bool {
	if need, err := xwal.NeedsFlatten(boot.AriaRoot()); err == nil && need {
		return true
	}
	_, holder, held := boot.StoreLockHolder()
	return held && holder == pid
}

func systemctlUserStart(u *daemonUnit) error {
	out, err := exec.Command("systemctl", "--user", "start", u.Name).CombinedOutput()
	if err != nil && len(out) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return err
}

func liveUnitWatch(u *daemonUnit, mayStart bool) unitWatch {
	sock := boot.AngelusSocketPath()
	ep := transport.UnixEndpoint(sock)
	return unitWatch{
		show:  func() *daemonUnit { return showDaemonUnit(u.Name, u.Scope) },
		start: systemctlUserStart,
		answers: func() bool {
			cli, err := sdk.DialAngelus(ep)
			if err != nil {
				return false
			}
			cli.Close()
			return true
		},
		working:  daemonAtWork,
		mayStart: mayStart,
		sock:     sock,
		say:      stderrw,
		dial:     50 * time.Millisecond,
		recheck:  time.Second,
		notice:   startupNoticeAfter,
		stall:    unitBindStall,
		limit:    startupHardCap,
	}
}

func awaitGoverningUnit() bool {
	u := governingUnit()
	if u == nil {
		return false
	}
	if err := liveUnitWatch(u, !envTruthy(os.Getenv("FIGARO_NO_SELF_SPAWN"))).await(u); err != nil {
		die("%s", err)
	}
	cli, st, err := dialAnswered(transport.UnixEndpoint(boot.AngelusSocketPath()))
	if err != nil {
		die("%s", err)
	}
	checkDaemonBuild(st)
	cli.Close()
	return true
}
