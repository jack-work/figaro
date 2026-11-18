package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/boot"
)

type daemonUnit struct {
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`
	Load        string   `json:"load_state"`
	Active      string   `json:"active_state"`
	Sub         string   `json:"sub_state"`
	MainPID     int      `json:"main_pid"`
	ExecPath    string   `json:"exec_path,omitempty"`
	NeedReload  bool     `json:"need_daemon_reload,omitempty"`
	TriggeredBy []string `json:"triggered_by,omitempty"`
	Env         []string `json:"-"`
	EnvFiles    []string `json:"-"`
	Unset       []string `json:"-"`
}

type socketUnit struct {
	Name   string   `json:"name"`
	Scope  string   `json:"scope"`
	Load   string   `json:"load_state"`
	Active string   `json:"active_state"`
	Sub    string   `json:"sub_state"`
	Listen []string `json:"listen,omitempty"`
}

func (s *socketUnit) holds(sockPath string) bool {
	if s == nil {
		return false
	}
	if len(s.Listen) == 0 {
		return true
	}
	clean := filepath.Clean(sockPath)
	resolved, _ := filepath.EvalSymlinks(clean)
	for _, raw := range s.Listen {
		entry := filepath.Clean(raw)
		if entry == clean || (resolved != "" && entry == resolved) {
			return true
		}
		if er, err := filepath.EvalSymlinks(entry); err == nil && (er == clean || (resolved != "" && er == resolved)) {
			return true
		}
	}
	return false
}

type unitVerdict struct {
	Unit        *daemonUnit `json:"unit,omitempty"`
	Socket      *socketUnit `json:"socket,omitempty"`
	SocketOwner int         `json:"socket_owner_pid"`
	OK          bool        `json:"ok"`
	Message     string      `json:"message"`
}

func daemonUnitName() string {
	if n := os.Getenv("FIGARO_SYSTEMD_UNIT"); n != "" {
		return n
	}
	return "figaro.service"
}

func parseUnitShow(out string) daemonUnit {
	var u daemonUnit
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "Id":
			u.Name = v
		case "LoadState":
			u.Load = v
		case "ActiveState":
			u.Active = v
		case "SubState":
			u.Sub = v
		case "Environment":
			u.Env = splitUnitEnvironment(v)
		case "EnvironmentFiles":
			if f, _, _ := strings.Cut(v, " ("); f != "" {
				u.EnvFiles = append(u.EnvFiles, f)
			}
		case "UnsetEnvironment":
			u.Unset = splitUnitEnvironment(v)
		case "ExecStart":
			u.ExecPath = execStartPath(v)
		case "NeedDaemonReload":
			u.NeedReload = v == "yes"
		case "MainPID":
			u.MainPID, _ = strconv.Atoi(v)
		case "TriggeredBy":
			u.TriggeredBy = strings.Fields(v)
		}
	}
	return u
}

func parseSocketShow(out string) socketUnit {
	var s socketUnit
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "Id":
			s.Name = v
		case "LoadState":
			s.Load = v
		case "ActiveState":
			s.Active = v
		case "SubState":
			s.Sub = v
		case "Listen":
			if p, _, ok := strings.Cut(v, " ("); ok && p != "" {
				s.Listen = append(s.Listen, p)
			}
		}
	}
	return s
}

func findDaemonUnit(name string) *daemonUnit {
	for _, scope := range []string{"user", "system"} {
		if u := showDaemonUnit(name, scope); u != nil {
			return u
		}
	}
	return nil
}

func showDaemonUnit(name, scope string) *daemonUnit {
	args := []string{"show", "-p", "Id,LoadState,ActiveState,SubState,MainPID,TriggeredBy,Environment,EnvironmentFiles,UnsetEnvironment,ExecStart,NeedDaemonReload", name}
	if scope == "user" {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return nil
	}
	u := parseUnitShow(string(out))
	if u.Load != "loaded" {
		return nil
	}
	u.Scope = scope
	return &u
}

func findSocketUnit(u *daemonUnit) *socketUnit {
	if u == nil {
		return nil
	}
	var names []string
	for _, t := range u.TriggeredBy {
		if strings.HasSuffix(t, ".socket") {
			names = append(names, t)
		}
	}
	companion := strings.TrimSuffix(u.Name, ".service") + ".socket"
	seen := false
	for _, n := range names {
		if n == companion {
			seen = true
		}
	}
	if !seen {
		names = append(names, companion)
	}
	for _, n := range names {
		if s := showSocketUnit(n, u.Scope); s != nil {
			return s
		}
	}
	return nil
}

func showSocketUnit(name, scope string) *socketUnit {
	args := []string{"show", "-p", "Id,LoadState,ActiveState,SubState,Listen", name}
	if scope == "user" {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return nil
	}
	s := parseSocketShow(string(out))
	if s.Load != "loaded" {
		return nil
	}
	s.Scope = scope
	return &s
}

func execStartPath(v string) string {
	for _, field := range strings.Split(strings.Trim(v, "{} "), " ; ") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(field), "path="); ok {
			return p
		}
	}
	return ""
}

func splitUnitEnvironment(v string) []string {
	var out []string
	var cur strings.Builder
	quoted, escaped, held := false, false, false
	for _, r := range v {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			quoted, held = !quoted, true
		case r == ' ' && !quoted:
			if cur.Len() > 0 || held {
				out = append(out, cur.String())
			}
			cur.Reset()
			held = false
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || held {
		out = append(out, cur.String())
	}
	return out
}

func judgeDaemonUnit(u *daemonUnit, owner int) unitVerdict {
	v := unitVerdict{Unit: u, SocketOwner: owner}
	switch {
	case u == nil && owner == 0:
		v.OK, v.Message = true, "no daemon unit and no daemon answering"
	case u == nil:
		v.OK, v.Message = true, fmt.Sprintf("no daemon unit; the daemon (pid %d) was started by a client", owner)
	case owner == 0 && u.Active == "active":
		v.Message = fmt.Sprintf("%s is active (pid %d) but nothing answers the socket", u.Name, u.MainPID)
	case owner == 0:
		v.Message = fmt.Sprintf("%s is %s and no daemon answers; start it: systemctl %s start %s", u.Name, u.Active, scopeFlag(u), u.Name)
	case u.Active != "active":
		v.Message = fmt.Sprintf("%s is %s, but a daemon outside it (pid %d) answers the socket: a client spawned it, so it lacks the unit's environment and limits and dies with that client's scope. Run `figaro stop`, then systemctl %s start %s", u.Name, u.Active, owner, scopeFlag(u), u.Name)
	case u.MainPID != owner:
		v.Message = fmt.Sprintf("%s runs pid %d, but pid %d answers the socket: the daemon on the socket is not the unit's", u.Name, u.MainPID, owner)
	default:
		v.OK, v.Message = true, fmt.Sprintf("%s (pid %d) owns the socket", u.Name, owner)
	}
	return v
}

type unitProbes struct {
	ownerPID  func(path string) (int, error)
	listening func(path string) (known, listening bool)
	pidFile   func() (int, error)
	alive     func(pid int) bool
	isManager func(scope string, pid int) bool
}

func liveUnitProbes() unitProbes {
	pidPath := filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid")
	return unitProbes{
		ownerPID:  socketOwnerPID,
		listening: socketListening,
		pidFile:   func() (int, error) { return readPidFile(pidPath) },
		alive:     pidAlive,
		isManager: isManagerOwner,
	}
}

func isManagerOwner(scope string, pid int) bool {
	if scope == "system" {
		return pid == 1
	}
	if pid <= 1 {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return err == nil && strings.TrimSpace(string(raw)) == "systemd"
}

func inspectDaemonUnit(u *daemonUnit, s *socketUnit, sockPath string, p unitProbes) unitVerdict {
	if s == nil || s.Active != "active" || !s.holds(sockPath) {
		owner, _ := p.ownerPID(sockPath)
		return judgeDaemonUnit(u, owner)
	}
	if u.Active != "active" {
		if recorded, err := p.pidFile(); err == nil && recorded > 0 && p.alive(recorded) {
			return unitVerdict{
				Unit:    u,
				Socket:  s,
				Message: fmt.Sprintf("%s is %s, but a daemon outside it (pid %d) holds angelus.pid: run `figaro stop`, then systemctl %s start %s", u.Name, u.Active, recorded, scopeFlag(u), s.Name),
			}
		}
		known, listening := p.listening(sockPath)
		if (known && !listening) || (!known && !fileExists(sockPath)) {
			return unitVerdict{
				Unit:    u,
				Socket:  s,
				Message: fmt.Sprintf("%s is active while %s is %s, but nothing listens on %s", s.Name, u.Name, u.Active, sockPath),
			}
		}
		owner := 0
		if u.Scope == "system" {
			owner = 1
		}
		return unitVerdict{
			Unit:        u,
			Socket:      s,
			SocketOwner: owner,
			OK:          true,
			Message:     fmt.Sprintf("%s holds the socket while %s is %s; the next call starts it", s.Name, u.Name, u.Active),
		}
	}

	owner, _ := p.ownerPID(sockPath)
	switch {
	case owner == 0:
		return unitVerdict{
			Unit:    u,
			Socket:  s,
			Message: fmt.Sprintf("%s is active (pid %d) under %s, but nothing answers the socket", u.Name, u.MainPID, s.Name),
		}
	case owner == u.MainPID:
		return unitVerdict{
			Unit:        u,
			Socket:      s,
			SocketOwner: owner,
			OK:          true,
			Message:     fmt.Sprintf("%s (pid %d) owns the socket", u.Name, owner),
		}
	case !p.isManager(u.Scope, owner):
		return unitVerdict{
			Unit:        u,
			Socket:      s,
			SocketOwner: owner,
			Message:     fmt.Sprintf("%s runs pid %d, but pid %d answers the socket: the daemon on the socket is not the unit's", u.Name, u.MainPID, owner),
		}
	}

	recorded, pidErr := p.pidFile()
	switch {
	case pidErr == nil && recorded != u.MainPID:
		return unitVerdict{
			Unit:        u,
			Socket:      s,
			SocketOwner: owner,
			Message:     fmt.Sprintf("%s runs pid %d, but angelus.pid names pid %d: the daemon on the socket is not the unit's", u.Name, u.MainPID, recorded),
		}
	case pidErr == nil || (errors.Is(pidErr, fs.ErrPermission) && u.MainPID > 0 && p.alive(u.MainPID)):
		return unitVerdict{
			Unit:        u,
			Socket:      s,
			SocketOwner: owner,
			OK:          true,
			Message:     fmt.Sprintf("%s (pid %d) serves %s's socket (held by pid %d)", u.Name, u.MainPID, s.Name, owner),
		}
	default:
		return unitVerdict{
			Unit:        u,
			Socket:      s,
			SocketOwner: owner,
			Message:     fmt.Sprintf("%s runs pid %d under %s, but angelus.pid is unreadable (%v)", u.Name, u.MainPID, s.Name, pidErr),
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func scopeFlag(u *daemonUnit) string {
	if u.Scope == "user" {
		return "--user"
	}
	return ""
}

func runDoctorUnit(asJSON bool) error {
	u := findDaemonUnit(daemonUnitName())
	v := inspectDaemonUnit(u, findSocketUnit(u), boot.AngelusSocketPath(), liveUnitProbes())
	if asJSON {
		return json.NewEncoder(stdout).Encode(v)
	}
	mark := "ok"
	if !v.OK {
		mark = "WARN"
	}
	fmt.Fprintf(stdout, "%s  %s\n", mark, v.Message)
	return nil
}
