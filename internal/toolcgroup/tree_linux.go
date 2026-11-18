//go:build linux

package toolcgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

const Mount = "/sys/fs/cgroup"

type Tree struct {
	root  string
	mount string
	reg   *Registry
	mu    sync.Mutex
	fds   map[string]int
}

func Own(reg *Registry) (*Tree, error) {
	cg, err := ProcCgroupOf(os.Getpid())
	if err != nil {
		return nil, err
	}
	return At(Mount, cg, reg)
}

func At(mount, root string, reg *Registry) (*Tree, error) {
	dir := filepath.Join(mount, root)
	if err := unix.Access(filepath.Join(dir, "cgroup.procs"), unix.W_OK); err != nil {
		return nil, fmt.Errorf("toolcgroup: this daemon may not create cgroups under %s (%v); run it in a scope or unit with Delegate=yes", root, err)
	}
	return &Tree{root: root, mount: mount, reg: reg, fds: map[string]int{}}, nil
}

func (t *Tree) Root() string { return t.root }

func (t *Tree) CgroupOf(figaro string) string {
	return filepath.Join(t.root, "tools-"+figaro)
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
	cg := t.CgroupOf(figaro)
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
