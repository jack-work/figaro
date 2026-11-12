//go:build !windows

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

type procInfo struct {
	PID, PPID, PGRP, SID, TPGID int
	Exe                         string
	Argv                        []string
	Cwd                         string
	Stdin                       string
	Env                         []string
	DroppedEnv                  []string
	Daemon                      bool
}

type procTable struct {
	byPID    map[int]procInfo
	children map[int][]int
}

func (t procTable) get(pid int) (procInfo, bool) {
	p, ok := t.byPID[pid]
	return p, ok
}

type tmuxPane struct {
	ID      string
	PID     int
	Command string
}

func isFigaroExe(exe string) bool { return filepath.Base(exe) == "figaro" }

func isShell(exe string) bool {
	switch strings.TrimPrefix(filepath.Base(exe), "-") {
	case "sh", "bash", "zsh", "fish", "dash", "ksh":
		return true
	}
	return false
}

func findClients(panes []tmuxPane, procs procTable, self int) (clients []paneClient, skipped []string) {
	for _, pane := range panes {
		if paneHolds(procs, pane.PID, self) {
			skipped = append(skipped, pane.ID+": runs this upgrade; run your command again there")
			continue
		}
		var walk func(pid int)
		walk = func(pid int) {
			p, ok := procs.get(pid)
			if !ok {
				return
			}
			if isFigaroExe(p.Exe) && !p.Daemon {
				clients = append(clients, clientOf(pane, p, procs))
				return
			}
			for _, c := range procs.children[pid] {
				walk(c)
			}
		}
		walk(pane.PID)
	}
	return clients, skipped
}

func paneHolds(procs procTable, root, pid int) bool {
	for pid > 1 {
		if pid == root {
			return true
		}
		p, ok := procs.get(pid)
		if !ok {
			return false
		}
		pid = p.PPID
	}
	return false
}

func clientOf(pane tmuxPane, p procInfo, procs procTable) paneClient {
	c := paneClient{
		Pane:       pane.ID,
		PID:        p.PID,
		SID:        p.SID,
		Exe:        p.Exe,
		Argv:       p.Argv,
		Cwd:        p.Cwd,
		Env:        p.Env,
		DroppedEnv: p.DroppedEnv,
		Own:        p.PID == pane.PID,
		StdinPiped: p.Stdin != "" && !strings.HasPrefix(p.Stdin, "/dev/pts/") && !strings.HasPrefix(p.Stdin, "/dev/tty"),
	}
	if parent, ok := procs.get(p.PPID); ok && isShell(parent.Exe) {
		c.ShellPID = parent.PID
		c.Shell = strings.TrimPrefix(filepath.Base(parent.Exe), "-")
	}
	return c
}

func readProcTable() (procTable, error) {
	t := procTable{byPID: map[int]procInfo{}, children: map[int][]int{}}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return t, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, ok := readProc(pid)
		if !ok {
			continue
		}
		t.byPID[pid] = p
		t.children[p.PPID] = append(t.children[p.PPID], pid)
	}
	for _, kids := range t.children {
		sort.Ints(kids)
	}
	return t, nil
}

func readProc(pid int) (procInfo, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return procInfo{}, false
	}
	p := procInfo{PID: pid}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return procInfo{}, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 6 {
		return procInfo{}, false
	}
	p.PPID, _ = strconv.Atoi(fields[1])
	p.PGRP, _ = strconv.Atoi(fields[2])
	p.SID, _ = strconv.Atoi(fields[3])
	p.TPGID, _ = strconv.Atoi(fields[5])
	p.Exe, _ = os.Readlink(filepath.Join(dir, "exe"))
	if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		raw = bytes.TrimRight(raw, "\x00")
		if len(raw) > 0 {
			p.Argv = strings.Split(string(raw), "\x00")
		}
	}
	p.Cwd, _ = os.Readlink(filepath.Join(dir, "cwd"))
	p.Stdin, _ = os.Readlink(filepath.Join(dir, "fd", "0"))
	if isFigaroExe(p.Exe) {
		env, _ := os.ReadFile(filepath.Join(dir, "environ"))
		p.Daemon = (len(p.Argv) > 1 && p.Argv[1] == "--angelus") || bytes.Contains(env, []byte("_FIGARO_DAEMON=1\x00"))
		p.Env, p.DroppedEnv = replayableEnv(strings.Split(string(env), "\x00"))
	}
	return p, true
}

func (o upgradeOptions) tmux(args ...string) *exec.Cmd {
	if o.tmuxSocket != "" {
		args = append([]string{"-L", o.tmuxSocket}, args...)
	}
	return exec.Command("tmux", args...)
}

func (o upgradeOptions) listPanes() ([]tmuxPane, error) {
	out, err := o.tmux("list-panes", "-a", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_current_command}").Output()
	if err != nil {
		return nil, nil
	}
	var panes []tmuxPane
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		panes = append(panes, tmuxPane{ID: f[0], PID: pid, Command: f[2]})
	}
	return panes, nil
}

func releaseOf(exe string) string {
	out, err := exec.Command(exe, "version", "-j").Output()
	if err != nil {
		return ""
	}
	var v struct {
		Release  string `json:"release"`
		Revision string `json:"revision"`
	}
	if json.Unmarshal(out, &v) != nil {
		return ""
	}
	if v.Release != "" {
		return v.Release
	}
	return v.Revision
}

func runningAngelus() (release string, up bool) {
	cli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return "", false
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return "", true
	}
	if st.Release != "" {
		return st.Release, true
	}
	return short12(st.Build), true
}

func busyArias(ctx context.Context) ([]string, error) {
	cli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return nil, nil
	}
	defer cli.Close()
	resp, err := cli.ListWith(ctx, rpc.ListRequest{})
	if err != nil {
		return nil, err
	}
	var busy []string
	for _, f := range resp.Figaros {
		if f.State == "active" {
			name := f.Handle
			if name == "" {
				name = short12(f.ID)
			}
			if f.Mantra != "" {
				name += " (" + f.Mantra + ")"
			}
			busy = append(busy, name)
		}
	}
	return busy, nil
}

func waitQuiet(o upgradeOptions, busy func() ([]string, error), tick time.Duration) error {
	deadline := time.Now().Add(o.wait)
	announced := ""
	for {
		names, err := busy()
		if err != nil {
			return fmt.Errorf("list arias: %w", err)
		}
		if len(names) == 0 {
			return nil
		}
		if now := strings.Join(names, ", "); now != announced {
			fmt.Fprintf(stderrw, "waiting for turns or queued prompts on: %s\n", now)
			announced = now
		}
		if !time.Now().Before(deadline) {
			if o.force {
				fmt.Fprintf(stderrw, "--force: going ahead; these turns are interrupted: %s\n", announced)
				return nil
			}
			return fmt.Errorf("still running after %s: %s (wait longer with --wait, or interrupt them with --force)", o.wait, announced)
		}
		time.Sleep(tick)
	}
}

func liveBusyArias() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return busyArias(ctx)
}

func telefonoLine() string {
	out, err := exec.Command("systemctl", "--user", "is-active", "telefono.service").Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	if strings.TrimSpace(string(out)) != "active" {
		return ""
	}
	return "telefono.service: attends an aria and dials the daemon per message, so it should carry on; if it reports errors, run: systemctl --user restart telefono"
}

func printPlan(steps []upgradeStep, skipped []string, extra []string) {
	if len(steps) == 0 && len(skipped) == 0 {
		fmt.Fprintln(stderrw, "no figaro clients in tmux panes")
	}
	for _, s := range steps {
		fmt.Fprintf(stderrw, "  %-7s %s  %s  (%s)\n", s.Action, s.Client.Pane, shownArgv(s.Client.Argv), s.Reason)
	}
	for _, s := range skipped {
		fmt.Fprintf(stderrw, "  skip    %s\n", s)
	}
	for _, e := range extra {
		fmt.Fprintf(stderrw, "  note    %s\n", e)
	}
}

func runUpgrade(o upgradeOptions) {
	to, err := upgradeTarget(o.to)
	if err != nil {
		die("%s", err)
	}
	if switchedAlready(o, to) {
		fmt.Fprintf(stderrw, "figaro: %s is the build already running; nothing to restart\n", to)
		return
	}
	running, up := runningAngelus()
	if err := boot.SpawnRefusal(os.Getenv); up && err != nil {
		die("%s", err)
	}
	if !up && !o.dryRun {
		recordSwitchExe(to)
		fmt.Fprintln(stderrw, "figaro: no angelus is running; the next figaro command starts the new build")
		return
	}
	toRelease := releaseOf(to)
	fmt.Fprintf(stderrw, "angelus %s -> %s (%s)\n", orUnknown(running), orUnknown(toRelease), to)

	panes, _ := o.listPanes()
	procs, err := readProcTable()
	if err != nil {
		die("read processes: %s", err)
	}
	clients, skipped := findClients(panes, procs, os.Getpid())
	recordReleases(clients, releaseOf)
	recordAttendance(clients)
	steps := make([]upgradeStep, len(clients))
	for i, c := range clients {
		steps[i] = planClient(c)
	}
	var notes []string
	if line := telefonoLine(); line != "" {
		notes = append(notes, line)
	}
	printPlan(steps, skipped, notes)
	if o.dryRun {
		return
	}

	if err := waitQuiet(o, liveBusyArias, 2*time.Second); err != nil {
		die("%s", err)
	}
	var unconfirmed []string
	for i := range steps {
		if steps[i].Action == upgradeRespawn {
			unconfirmed = append(unconfirmed, o.stopClient(&steps[i])...)
		}
	}
	runStop(stopOptions{keepAgent: true})
	start := exec.Command(to, "ls", "-j")
	start.Env = append(os.Environ(), boot.BootReasonEnv+"=upgrade")
	start.Stdout = nil
	start.Stderr = stderrw
	if err := start.Run(); err != nil {
		die("start the new angelus with %s: %s", to, err)
	}
	report := make([]string, 0, len(steps))
	for i := range steps {
		report = append(report, o.respawnClient(steps[i]))
	}
	for _, r := range report {
		fmt.Fprintln(stderrw, r)
	}
	for _, s := range skipped {
		fmt.Fprintln(stderrw, "skipped: "+s)
	}
	for _, n := range notes {
		fmt.Fprintln(stderrw, "note: "+n)
	}
	for _, u := range unconfirmed {
		fmt.Fprintln(stderrw, u)
	}
	recordSwitchExe(to)
	fmt.Fprintf(stderrw, "figaro %s is running\n", orUnknown(toRelease))
}

func recordReleases(clients []paneClient, release func(exe string) string) {
	known := map[string]string{}
	for i := range clients {
		exe := clients[i].Exe
		r, ok := known[exe]
		if !ok {
			r = release(exe)
			known[exe] = r
		}
		clients[i].Release = r
	}
}

func recordAttendance(clients []paneClient) {
	cli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return
	}
	defer cli.Close()
	for i := range clients {
		session := clients[i].SID
		if session <= 0 {
			session = clients[i].PID
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := cli.Resolve(ctx, session)
		cancel()
		if err == nil && resp.Found {
			clients[i].Attends = resp.FigaroID
		}
	}
}

func switchedAlready(o upgradeOptions, to string) bool {
	return o.ifChanged && !o.dryRun && lastSwitchExe() == to
}

func switchStatePath() string { return filepath.Join(boot.StateDir(), "switched-to") }

func lastSwitchExe() string {
	raw, err := os.ReadFile(switchStatePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func recordSwitchExe(exe string) {
	if err := os.MkdirAll(filepath.Dir(switchStatePath()), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(switchStatePath(), []byte(exe+"\n"), 0o600)
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown build)"
	}
	return s
}

func upgradeTarget(to string) (string, error) {
	if to == "" {
		return "", fmt.Errorf("give the new build with --to <path to its figaro binary>")
	}
	abs, err := filepath.Abs(to)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable figaro", abs)
	}
	return abs, nil
}

func (o upgradeOptions) stopClient(s *upgradeStep) []string {
	c := s.Client
	if c.Own {
		_ = o.tmux("set-option", "-p", "-t", c.Pane, "remain-on-exit", "on").Run()
	}
	_ = killPid(c.PID, syscall.SIGTERM)
	if !waitForExit(c.PID, 10*time.Second) {
		s.Action, s.Reason = upgradeLeave, "did not exit within 10s of SIGTERM"
		return nil
	}
	out, err := o.tmux("capture-pane", "-p", "-J", "-t", c.Pane, "-S", "-50").Output()
	if err != nil {
		return nil
	}
	return unconfirmedLines(c.Pane, string(out))
}

func unconfirmedLines(pane, screen string) []string {
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "not confirmed") {
			lines = append(lines, pane+": "+strings.TrimSpace(l))
		}
	}
	return lines
}

func respawnArgs(s upgradeStep) []string {
	c := s.Client
	args := []string{"respawn-pane", "-k", "-t", c.Pane, "-c", c.Cwd}
	for _, kv := range c.Env {
		args = append(args, "-e", kv)
	}
	if c.Attends != "" {
		args = append(args, "-e", handedAttendanceEnv+"="+c.Attends)
	}
	args = append(args, "--")
	if len(s.Argv) == 1 {
		args = append(args, "env", "--")
	}
	return append(args, s.Argv...)
}

func (o upgradeOptions) respawnClient(s upgradeStep) string {
	c := s.Client
	line := shownArgv(c.Argv)
	switch s.Action {
	case upgradeLeave:
		return fmt.Sprintf("left %s (%s): %s; restart it yourself", c.Pane, line, s.Reason)
	case upgradeKeep:
		return fmt.Sprintf("kept %s (%s): %s", c.Pane, line, s.Reason)
	}
	line = shownArgv(s.Argv)
	if c.Own {
		err := o.tmux(respawnArgs(s)...).Run()
		_ = o.tmux("set-option", "-p", "-u", "-t", c.Pane, "remain-on-exit").Run()
		if err != nil {
			return fmt.Sprintf("left %s (%s): respawn-pane failed: %s", c.Pane, line, err)
		}
		if len(c.DroppedEnv) > 0 {
			return fmt.Sprintf("respawned %s: %s (not carried over: %s; set them in the pane if it needs them)", c.Pane, line, strings.Join(c.DroppedEnv, ", "))
		}
		return fmt.Sprintf("respawned %s: %s", c.Pane, line)
	}
	shell, ok := readProc(c.ShellPID)
	if !ok || shell.TPGID != shell.PGRP {
		return fmt.Sprintf("left %s (%s): its shell does not hold the terminal; type the command again", c.Pane, line)
	}
	if err := o.tmux("send-keys", "-t", c.Pane, "-l", commandLineFor(s.Argv, c.Shell)).Run(); err != nil {
		return fmt.Sprintf("left %s (%s): send-keys failed: %s", c.Pane, line, err)
	}
	if err := o.tmux("send-keys", "-t", c.Pane, "Enter").Run(); err != nil {
		return fmt.Sprintf("left %s (%s): send-keys failed: %s", c.Pane, line, err)
	}
	return fmt.Sprintf("restarted %s: %s", c.Pane, line)
}
