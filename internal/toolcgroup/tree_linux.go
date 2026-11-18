//go:build linux

package toolcgroup

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

const Mount = "/sys/fs/cgroup"

type Tree struct {
	mount string
	reg   *Registry
	mu    sync.Mutex
	root  string
	err   error
	fds   map[string]int
}

func Own(reg *Registry) *Tree {
	return &Tree{mount: Mount, reg: reg, fds: map[string]int{}}
}

func At(mount, root string, reg *Registry) *Tree {
	return &Tree{mount: mount, root: root, reg: reg, fds: map[string]int{}}
}

func (t *Tree) Root() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.placed()
}

func (t *Tree) placed() (string, error) {
	if t.root == "" && t.err == nil {
		t.root, t.err = ProcCgroupOf(os.Getpid())
		if t.err == nil {
			t.err = writable(filepath.Join(t.mount, t.root), t.root)
		}
		if t.err != nil {
			slog.Error("peer check: tools cannot start until this daemon may create cgroups; [authz] peer_check = false runs them unattributed", "err", t.err)
		} else {
			slog.Info("peer check: each figaro's tools run in its own cgroup", "under", t.root)
		}
	}
	return t.root, t.err
}

func writable(dir, root string) error {
	if err := unix.Access(filepath.Join(dir, "cgroup.procs"), unix.W_OK); err != nil {
		return fmt.Errorf("toolcgroup: this daemon may not create cgroups under %s (%v); run it in a scope or unit with Delegate=yes", root, err)
	}
	return nil
}

func cgroupOf(root, figaro string) string {
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
	cg := cgroupOf(root, figaro)
	dir := filepath.Join(t.mount, cg)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return -1, fmt.Errorf("toolcgroup: %w", err)
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
