package agent

import (
	"bufio"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/box"
)

const probeTimeout = 60 * time.Second

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func ProbeScript(writable []string) string {
	var dirs []string
	for _, d := range writable {
		dirs = append(dirs, quote(d))
	}
	list := strings.Join(dirs, " ")
	return strings.Join([]string{
		`echo "self $$ $PPID $(id -u)"`,
		`for d in ` + list + `; do find "$d" -mindepth 1 -maxdepth 1 -print 2>/dev/null | sed 's/^/entry /'; done`,
		`find / \( -path /proc -o -path /sys -o -path /dev -o -path /nix \) -prune -o ! -type l -writable -print 2>/dev/null | sed 's/^/writable /'`,
		`find /dev/shm ! -type l -writable -print 2>/dev/null | sed 's/^/writable /'`,
		`for p in /proc/[0-9]*; do u=$(awk '/^Uid:/{print $2}' "$p/status" 2>/dev/null); pp=$(awk '/^PPid:/{print $2}' "$p/status" 2>/dev/null); c=$(cat "$p/comm" 2>/dev/null); [ -n "$u" ] && echo "proc ${p#/proc/} $pp $u $c"; done`,
		`echo end`,
	}, "\n")
}

func ParseProbe(out string, writable []string) error {
	var self, parent, uid string
	var dirt []string
	ended := false
	type proc struct{ pid, ppid, uid, comm string }
	var procs []proc
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		kind, rest, _ := strings.Cut(sc.Text(), " ")
		switch kind {
		case "self":
			f := strings.Fields(rest)
			if len(f) == 3 {
				self, parent, uid = f[0], f[1], f[2]
			}
		case "entry":
			dirt = append(dirt, "left in a writable dir: "+rest)
		case "writable":
			if !slices.Contains(writable, strings.TrimRight(rest, "/")) {
				dirt = append(dirt, "writable outside the class's list: "+rest)
			}
		case "proc":
			f := strings.SplitN(rest, " ", 4)
			if len(f) == 4 {
				procs = append(procs, proc{f[0], f[1], f[2], f[3]})
			}
		case "end":
			ended = true
		}
	}
	if !ended || self == "" {
		return fmt.Errorf("probe: the sandbox did not run the probe to its end")
	}
	for _, p := range procs {
		if p.uid != uid || p.pid == self || p.pid == parent || p.ppid == self {
			continue
		}
		dirt = append(dirt, fmt.Sprintf("process %s (%s) of the sandbox uid outlived the reset", p.pid, p.comm))
	}
	if len(dirt) > 0 {
		if len(dirt) > 8 {
			dirt = append(dirt[:8], fmt.Sprintf("and %d more", len(dirt)-8))
		}
		return fmt.Errorf("probe: %s", strings.Join(dirt, "; "))
	}
	return nil
}

func Probe(ctx context.Context, name, exec string, writable []string, figaro string) error {
	path, err := SocketPath(exec)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	b, err := box.Provider{Name: name, Socket: path}.Open(ctx, sandbox.Spec{Figaro: figaro})
	if err != nil {
		return fmt.Errorf("probe %s: %w", name, err)
	}
	defer b.Close()
	var out strings.Builder
	p, err := b.Exec(ctx, sandbox.ExecRequest{Command: ProbeScript(writable), OnChunk: func(c []byte) {
		if out.Len() < 4<<20 {
			out.Write(c)
		}
	}})
	if err != nil {
		return fmt.Errorf("probe %s: %w", name, err)
	}
	if r := p.Wait(); r.TimedOut || r.Canceled {
		return fmt.Errorf("probe %s: did not finish", name)
	}
	if err := ParseProbe(out.String(), writable); err != nil {
		return fmt.Errorf("%s %w", name, err)
	}
	return nil
}
