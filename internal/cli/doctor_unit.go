package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/boot"
)

type daemonUnit struct {
	Name    string   `json:"name"`
	Scope   string   `json:"scope"`
	Load    string   `json:"load_state"`
	Active  string   `json:"active_state"`
	Sub     string   `json:"sub_state"`
	MainPID int      `json:"main_pid"`
	Env     []string `json:"-"`
}

type unitVerdict struct {
	Unit        *daemonUnit `json:"unit,omitempty"`
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
		case "MainPID":
			u.MainPID, _ = strconv.Atoi(v)
		}
	}
	return u
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
	args := []string{"show", "-p", "Id,LoadState,ActiveState,SubState,MainPID,Environment", name}
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

func scopeFlag(u *daemonUnit) string {
	if u.Scope == "user" {
		return "--user"
	}
	return ""
}

func runDoctorUnit(asJSON bool) error {
	owner, _ := socketOwnerPID(boot.AngelusSocketPath())
	v := judgeDaemonUnit(findDaemonUnit(daemonUnitName()), owner)
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
