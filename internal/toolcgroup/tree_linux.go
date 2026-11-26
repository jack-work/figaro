//go:build linux

package toolcgroup

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const Mount = "/sys/fs/cgroup"

type Tree struct {
	mount string
	reg   *Registry
	lim   Limits
	mu    sync.Mutex
	root  string
	err   error
	done  bool
	cpu   bool
	fds   map[string]int
	rmdir func(string) error
	move  func(pid, toDir string) error
}

func Own(reg *Registry, lim Limits) *Tree {
	return &Tree{mount: Mount, reg: reg, lim: lim, fds: map[string]int{}, rmdir: unix.Rmdir, move: moveTo}
}

func At(mount, root string, reg *Registry, lim Limits) *Tree {
	return &Tree{mount: mount, root: root, reg: reg, lim: lim, fds: map[string]int{}, rmdir: unix.Rmdir, move: moveTo}
}

func (t *Tree) Root() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.placed()
}

func (t *Tree) placed() (string, error) {
	if t.done {
		return t.root, t.err
	}
	t.done = true
	if t.root == "" {
		t.root, t.err = ProcCgroupOf(os.Getpid())
		if t.err == nil && !t.lim.Parented() {
			t.err = writable(filepath.Join(t.mount, t.root), t.root)
		}
	}
	if t.err == nil && t.lim.Parented() {
		t.root, t.err = t.arrange(t.root)
	}
	if t.err != nil {
		slog.Error("peer check: tools cannot start until this daemon may create cgroups; [authz] peer_check = false runs them unattributed", "err", t.err)
	} else {
		slog.Info("peer check: each figaro's tools run in its own cgroup", "under", t.root)
	}
	return t.root, t.err
}

func (t *Tree) arrange(own string) (string, error) {
	unit := own
	if filepath.Base(own) == "daemon" {
		unit = filepath.Dir(own)
	}
	u := filepath.Join(t.mount, unit)
	if err := writable(u, unit); err != nil {
		return "", err
	}
	d := filepath.Join(u, "daemon")
	if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("toolcgroup: %w", err)
	}
	want := []string{"+cpu"}
	if t.lim.ParentMemoryHigh != "" {
		if hasController(filepath.Join(u, "cgroup.controllers"), "memory") {
			want = append(want, "+memory")
		} else {
			slog.Error("toolcgroup: memory is not delegated to this unit (Delegate= without memory): parent_memory_high is not applied", "unit", unit)
		}
	}
	enabled := false
	for range 5 {
		t.drain(u, d)
		if err := t.move(strconv.Itoa(os.Getpid()), d); err != nil {
			return "", fmt.Errorf("toolcgroup: move this daemon to %s/daemon: %w", unit, err)
		}
		if err := writeFile(filepath.Join(u, "cgroup.subtree_control"), strings.Join(want, " ")); err == nil {
			enabled = true
			break
		}
	}
	if !enabled {
		slog.Error("toolcgroup: the unit keeps a process, so its children get no cpu controller: tool limits are not applied", "unit", unit)
	}
	tools := filepath.Join(u, "tools")
	if err := os.Mkdir(tools, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("toolcgroup: %w", err)
	}
	if enabled {
		t.limit(d, "cpu.weight", weight(t.lim.DaemonCPUWeight))
		t.limit(tools, "cpu.max", t.lim.ParentCPUMax)
		if len(want) > 1 {
			t.limit(tools, "memory.high", t.lim.ParentMemoryHigh)
		}
		if err := writeFile(filepath.Join(tools, "cgroup.subtree_control"), "+cpu"); err != nil {
			slog.Error("toolcgroup: +cpu below the tools parent refused: per-tool limits are not applied", "err", err)
		} else {
			t.cpu = true
		}
	}
	t.reg.Guard(unit, os.Getpid())
	t.adopt(unit, u, tools)
	return filepath.Join(unit, "tools"), nil
}

func (t *Tree) adopt(unit, u, tools string) {
	old, _ := filepath.Glob(filepath.Join(u, "tools-*"))
	var left []string
	for _, dir := range old {
		aria := strings.TrimPrefix(filepath.Base(dir), "tools-")
		if aria == "" || aria == "." || aria == ".." {
			continue
		}
		if len(procsOf(dir)) > 0 {
			dst := filepath.Join(tools, aria)
			if err := os.Mkdir(dst, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				slog.Error("toolcgroup: cannot adopt an earlier daemon's tool cgroup", "cgroup", dir, "err", err)
			} else {
				if t.cpu {
					t.limit(dst, "cpu.max", t.lim.ToolCPUMax)
					t.limit(dst, "cpu.weight", weight(t.lim.ToolCPUWeight))
				}
				if _, err := t.reg.Register(filepath.Join(unit, "tools", aria), aria); err != nil {
					slog.Error("toolcgroup: cannot register an adopted tool cgroup", "aria", aria, "err", err)
				}
				t.drain(dir, dst)
			}
		}
		if err := t.rmdir(dir); err != nil {
			left = append(left, dir)
			if _, rerr := t.reg.Register(filepath.Join(unit, filepath.Base(dir)), aria); rerr != nil {
				slog.Error("toolcgroup: cannot register an earlier daemon's tool cgroup", "aria", aria, "err", rerr)
			}
			t.limit(dir, "cpu.max", t.lim.ToolCPUMax)
			t.limit(dir, "cpu.weight", weight(t.lim.ToolCPUWeight))
			slog.Error("toolcgroup: an earlier daemon's tool cgroup stays populated, outside the tools parent's limits until its shells exit", "cgroup", dir, "err", err)
		}
	}
	if len(old) > 0 {
		slog.Info("toolcgroup: adopted an earlier daemon's tool cgroups under the parent", "found", len(old), "left", len(left))
	}
}

func (t *Tree) drain(from, to string) {
	for range 5 {
		pids := procsOf(from)
		if len(pids) == 0 {
			return
		}
		for _, pid := range pids {
			if err := t.move(pid, to); err != nil {
				slog.Warn("toolcgroup: a process did not move", "pid", pid, "from", from, "err", err)
			}
		}
	}
}

func procsOf(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

func moveTo(pid, toDir string) error { return writeFile(filepath.Join(toDir, "cgroup.procs"), pid) }

func (t *Tree) limit(dir, file, val string) {
	if val == "" {
		return
	}
	if err := writeFile(filepath.Join(dir, file), val); err != nil {
		slog.Error("toolcgroup: limit refused", "file", filepath.Join(dir, file), "value", val, "err", err)
	}
}

func weight(w int) string {
	if w == 0 {
		return ""
	}
	return strconv.Itoa(w)
}

func writeFile(path, val string) error {
	return os.WriteFile(path, []byte(val), 0o644)
}

func hasController(path, name string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && slices.Contains(strings.Fields(string(raw)), name)
}

func writable(dir, root string) error {
	if err := unix.Access(filepath.Join(dir, "cgroup.procs"), unix.W_OK); err != nil {
		return fmt.Errorf("toolcgroup: this daemon may not create cgroups under %s (%v); run it in a scope or unit with Delegate=yes", root, err)
	}
	return nil
}

func (t *Tree) cgroupOf(root, figaro string) string {
	if t.lim.Parented() {
		return filepath.Join(root, figaro)
	}
	return filepath.Join(root, "tools-"+figaro)
}

func (t *Tree) FD(figaro string) (int, error) {
	if figaro == "" || filepath.Base(figaro) != figaro || figaro == "." || figaro == ".." {
		return -1, fmt.Errorf("toolcgroup: %q is not a figaro id", figaro)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if fd, ok := t.fds[figaro]; ok {
		return fd, nil
	}
	root, err := t.placed()
	if err != nil {
		return -1, err
	}
	cg := t.cgroupOf(root, figaro)
	dir := filepath.Join(t.mount, cg)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return -1, fmt.Errorf("toolcgroup: %w", err)
	}
	if t.cpu {
		t.limit(dir, "cpu.max", t.lim.ToolCPUMax)
		t.limit(dir, "cpu.weight", weight(t.lim.ToolCPUWeight))
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("toolcgroup: open %s: %w", dir, err)
	}
	if _, err := t.reg.Register(cg, figaro); err != nil {
		unix.Close(fd)
		return -1, err
	}
	t.fds[figaro] = fd
	return fd, nil
}

func ProcCgroupOf(pid int) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return "", fmt.Errorf("toolcgroup: %w", err)
	}
	cg, ok := ParseProcCgroup(raw)
	if !ok {
		return "", fmt.Errorf("toolcgroup: pid %d is on no unified cgroup", pid)
	}
	return cg, nil
}
