package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/valet"
)

type socketProbe struct {
	Name    string `json:"name,omitempty"`
	Socket  string `json:"socket"`
	Answers bool   `json:"answers"`
	Error   string `json:"error,omitempty"`
}

type modeReport struct {
	Mode      string        `json:"mode"`
	Socket    string        `json:"socket"`
	DaemonUID int           `json:"daemon_uid"`
	Detail    string        `json:"detail"`
	Userns    bool          `json:"userns"`
	UsernsWhy string        `json:"userns_detail,omitempty"`
	Valet     socketProbe   `json:"valet"`
	Boxes     []socketProbe `json:"boxes,omitempty"`
}

func probeSocket(name, path string) socketProbe {
	p := socketProbe{Name: name, Socket: path}
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	c.Close()
	p.Answers = true
	return p
}

func userName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func judgeMode(sock string, me int, selfSpawn bool) modeReport {
	r := modeReport{Socket: sock, DaemonUID: -1}
	fi, err := os.Stat(sock)
	switch {
	case errors.Is(err, os.ErrNotExist) && selfSpawn:
		r.Mode, r.Detail = "home", "no daemon is running; the next command starts one as you"
	case err != nil:
		r.Mode, r.Detail = "unknown", fmt.Sprintf("no daemon socket: %v", err)
	default:
		uid, ok := boot.FileOwner(fi)
		if !ok {
			r.Mode, r.Detail = "unknown", "the daemon socket's owner is not readable here"
			break
		}
		r.DaemonUID = uid
		if r.DaemonUID == me {
			r.Mode, r.Detail = "home", "the daemon runs as you: its tools are you unless an outfit names a box"
		} else {
			r.Mode, r.Detail = "house", fmt.Sprintf("the daemon runs as %s (uid %d), not as you", userName(r.DaemonUID), r.DaemonUID)
		}
	}
	return r
}

func runDoctorMode(loaded *config.Loaded, asJSON bool) error {
	me := os.Getuid()
	r := judgeMode(boot.AngelusSocketPath(), me, os.Getenv("FIGARO_NO_SELF_SPAWN") != "1")
	if err := valet.Available(); err != nil {
		r.UsernsWhy = err.Error()
	} else {
		r.Userns = true
	}
	r.Valet = probeSocket("", filepath.Join("/run/figaro-valet", userName(me), "exec.sock"))
	if loaded != nil {
		for _, name := range loaded.BoxNames() {
			if b, ok := loaded.Box(name); ok && b.Exec != "" {
				r.Boxes = append(r.Boxes, probeSocket(name, b.Exec))
			}
		}
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(r)
	}
	fmt.Fprintf(stdout, "mode    %s: %s (%s)\n", r.Mode, r.Detail, r.Socket)
	if r.Userns {
		fmt.Fprintln(stdout, "userns  available: a valet can build its fence")
	} else {
		fmt.Fprintf(stdout, "userns  OFF: %s\n", r.UsernsWhy)
	}
	for _, p := range append([]socketProbe{r.Valet}, r.Boxes...) {
		label := "valet"
		if p.Name != "" {
			label = "box " + p.Name
		}
		if p.Answers {
			fmt.Fprintf(stdout, "%-7s %s answers\n", label, p.Socket)
		} else {
			fmt.Fprintf(stdout, "%-7s %s does not answer: %s\n", label, p.Socket, p.Error)
		}
	}
	return nil
}
