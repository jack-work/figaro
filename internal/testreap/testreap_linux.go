//go:build linux

package testreap

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type proc struct {
	pid   int
	pgid  int
	start string
	args  string
}

type Owner struct {
	hold   *os.File
	reaper *exec.Cmd
}

func Start() *Owner {
	if tag := os.Getenv(reaperEnv); tag != "" {
		os.Exit(reapAfterParent(tag))
	}
	owner = newOwner()
	r, w, err := os.Pipe()
	if err != nil {
		panic(fmt.Sprintf("testreap: pipe: %v", err))
	}
	exe, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("testreap: executable: %v", err))
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), reaperEnv+"="+owner)
	cmd.Stdin = r
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		panic(fmt.Sprintf("testreap: start the reaper: %v", err))
	}
	r.Close()
	if err := os.Setenv(OwnerEnv, owner); err != nil {
		panic(err)
	}
	return &Owner{hold: w, reaper: cmd}
}

func (o *Owner) Check(code int) int {
	if left := outliving(owner, Grace, o.reaper.Process.Pid); len(left) > 0 {
		fmt.Fprintf(os.Stderr, "testreap: %d processes outlived every test in this package; ending them:\n%s",
			len(left), describe(left))
		end(owner, left, o.reaper.Process.Pid)
		code = failed(code)
	}
	if z := unwaited(Grace, o.reaper.Process.Pid); len(z) > 0 {
		fmt.Fprintf(os.Stderr, "testreap: %d children exited and were never waited for:\n%s", len(z), describe(z))
		for _, p := range z {
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(p.pid, &ws, syscall.WNOHANG, nil)
		}
		code = failed(code)
	}
	o.hold.Close()
	done := make(chan struct{})
	go func() { _ = o.reaper.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		fmt.Fprintln(os.Stderr, "testreap: the reaper did not exit")
		code = failed(code)
	}
	return code
}

func failed(code int) int {
	if code == 0 {
		return 1
	}
	return code
}

func reapAfterParent(tag string) int {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	_, _ = io.Copy(io.Discard, os.Stdin)
	left := outliving(tag, 0, os.Getppid())
	if len(left) == 0 {
		return 0
	}
	fmt.Fprintf(os.Stderr, "testreap: the test binary ended without its cleanups; ending %d processes it started:\n%s",
		len(left), describe(left))
	end(tag, left, os.Getppid())
	return 0
}

func outliving(tag string, grace time.Duration, spare int) []proc {
	deadline := time.Now().Add(grace)
	for {
		left := marked(tag, spare)
		if len(left) == 0 || !time.Now().Before(deadline) {
			return left
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func end(tag string, ps []proc, spare int) {
	for round := 0; round < 3 && len(ps) > 0; round++ {
		signalAll(ps, syscall.SIGTERM)
		if !gone(ps, 3*time.Second) {
			signalAll(ps, syscall.SIGKILL)
			gone(ps, 5*time.Second)
		}
		ps = marked(tag, spare)
	}
}

func signalAll(ps []proc, sig syscall.Signal) {
	self := syscall.Getpgrp()
	for _, p := range ps {
		if !p.alive() {
			continue
		}
		if p.pgid == p.pid && p.pgid != self {
			_ = syscall.Kill(-p.pgid, sig)
		}
		_ = syscall.Kill(p.pid, sig)
	}
}

func gone(ps []proc, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		alive := false
		for _, p := range ps {
			if p.alive() {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p proc) alive() bool {
	st, ok := readStat(p.pid)
	return ok && st.start == p.start && st.state != "Z"
}

type stat struct {
	state string
	ppid  int
	pgid  int
	start string
}

func readStat(pid int) (stat, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return stat{}, false
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return stat{}, false
	}
	f := strings.Fields(string(raw[i+1:]))
	if len(f) < 20 {
		return stat{}, false
	}
	ppid, _ := strconv.Atoi(f[1])
	pgid, _ := strconv.Atoi(f[2])
	return stat{state: f[0], ppid: ppid, pgid: pgid, start: f[19]}, true
}

func pids() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

func markerOf(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return ""
	}
	value := ""
	for _, kv := range bytes.Split(raw, []byte{0}) {
		if v, ok := bytes.CutPrefix(kv, []byte(OwnerEnv+"=")); ok {
			value = string(v)
		}
	}
	return value
}

func marked(tag string, spare int) []proc {
	self := os.Getpid()
	var out []proc
	for _, pid := range pids() {
		if pid == self || pid == spare || !owns(markerOf(pid), tag) {
			continue
		}
		st, ok := readStat(pid)
		if !ok || st.state == "Z" {
			continue
		}
		out = append(out, proc{pid: pid, pgid: st.pgid, start: st.start, args: cmdline(pid)})
	}
	return out
}

func unwaited(grace time.Duration, spare int) []proc {
	self := os.Getpid()
	deadline := time.Now().Add(grace)
	for {
		var out []proc
		for _, pid := range pids() {
			if pid == spare {
				continue
			}
			if st, ok := readStat(pid); ok && st.ppid == self && st.state == "Z" {
				out = append(out, proc{pid: pid, pgid: st.pgid, start: st.start, args: cmdline(pid)})
			}
		}
		if len(out) == 0 || !time.Now().Before(deadline) {
			return out
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func cmdline(pid int) string {
	raw, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	s := strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
	if s == "" {
		comm, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
		s = "[" + strings.TrimSpace(string(comm)) + "]"
	}
	return s
}
