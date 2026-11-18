//go:build linux

package valet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var systemTrees = []string{"/bin", "/etc", "/lib", "/lib64", "/nix", "/opt", "/sbin", "/usr"}

var systemHidden = []string{"/nix/var/nix/daemon-socket"}

var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

func Available() error {
	for _, knob := range []struct{ path, off, why string }{
		{"/proc/sys/user/max_user_namespaces", "0", "user.max_user_namespaces is 0"},
		{"/proc/sys/kernel/unprivileged_userns_clone", "0", "kernel.unprivileged_userns_clone is 0"},
		{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns", "1", "kernel.apparmor_restrict_unprivileged_userns is 1"},
	} {
		raw, err := os.ReadFile(knob.path)
		if err == nil && strings.TrimSpace(string(raw)) == knob.off {
			return fmt.Errorf("valet: unprivileged user namespaces are off (%s), so the fence cannot be built and no tool runs", knob.why)
		}
	}
	return nil
}

func (f Fence) Build() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("fence: make / private: %w", err)
	}
	root := f.Root
	if err := tmpfs(root, "0755"); err != nil {
		return err
	}
	rootFd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("fence: open the new root: %w", err)
	}
	defer unix.Close(rootFd)
	for _, p := range systemTrees {
		if _, err := os.Stat(p); err == nil {
			if err := bind(rootFd, p, p, unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV, false); err != nil {
				return err
			}
		}
	}
	for _, p := range systemHidden {
		if isDir(filepath.Join(root, p)) {
			if err := tmpfs(filepath.Join(root, p), "0755"); err != nil {
				return err
			}
		}
	}
	steps := []func(string) error{runLinks, devices, procfs, scratchDirs}
	for _, step := range steps {
		if err := step(root); err != nil {
			return err
		}
	}
	if f.Home != "" {
		if err := mkdirIn(root, f.Home); err != nil {
			return err
		}
		if err := tmpfs(filepath.Join(root, f.Home), "0700"); err != nil {
			return err
		}
	}
	for _, g := range f.ordered() {
		attrs := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV)
		if !g.Write {
			attrs |= unix.MOUNT_ATTR_RDONLY
		}
		if err := bind(rootFd, g.Path, g.Path, attrs, true); err != nil {
			if errors.Is(err, errMoved) {
				fmt.Fprintf(os.Stderr, "valet: grant of %s skipped: %v\n", g.Path, err)
				continue
			}
			return err
		}
	}
	for _, p := range f.Hide {
		if err := hide(rootFd, p); err != nil {
			return err
		}
	}
	if f.Scratch != "" {
		if err := bind(rootFd, f.Scratch, f.Scratch, unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV, false); err != nil {
			return err
		}
	}
	if f.Door != "" {
		if err := bind(rootFd, f.Door, f.Door, unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV|unix.MOUNT_ATTR_NOEXEC, false); err != nil {
			return err
		}
	}
	return pivot(root)
}

func tmpfs(target, mode string) error {
	if err := unix.Mount("valet", target, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode="+mode); err != nil {
		return fmt.Errorf("fence: tmpfs on %s: %w", target, err)
	}
	return nil
}

func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func mkdirIn(root, p string) error {
	if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	return nil
}

var errMoved = errors.New("a symlink now stands on its path")

func beneath(dirFd int, name string, flags int) (int, error) {
	return unix.Openat2(dirFd, name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func openSource(src string, exact bool) (int, error) {
	how := &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC}
	if exact {
		how.Resolve = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, src, how)
	if errors.Is(err, unix.ELOOP) {
		return -1, fmt.Errorf("fence: %s: %w", src, errMoved)
	}
	if err != nil {
		return -1, fmt.Errorf("fence: open %s: %w", src, err)
	}
	if exact {
		if got, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd)); err != nil || got != src {
			unix.Close(fd)
			return -1, fmt.Errorf("fence: %s resolves to %q: %w", src, got, errMoved)
		}
	}
	return fd, nil
}

func mountPoint(rootFd int, path string, dir bool) (int, error) {
	at, err := unix.Dup(rootFd)
	if err != nil {
		return -1, fmt.Errorf("fence: %w", err)
	}
	parts := strings.Split(strings.Trim(filepath.Clean(path), "/"), "/")
	for i, name := range parts {
		last := i == len(parts)-1
		var made error
		if !last || dir {
			made = unix.Mkdirat(at, name, 0o755)
		} else if fd, err := unix.Openat(at, name, unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_RDONLY|unix.O_CLOEXEC, 0o644); err == nil {
			unix.Close(fd)
		} else {
			made = err
		}
		if made != nil && !errors.Is(made, unix.EEXIST) {
			unix.Close(at)
			return -1, fmt.Errorf("fence: mount point %s: %w", path, made)
		}
		flags := unix.O_PATH
		if !last || dir {
			flags |= unix.O_DIRECTORY
		}
		next, err := beneath(at, name, flags)
		unix.Close(at)
		if errors.Is(err, unix.ELOOP) {
			return -1, fmt.Errorf("fence: mount point %s: %w", path, errMoved)
		}
		if err != nil {
			return -1, fmt.Errorf("fence: mount point %s: %w", path, err)
		}
		at = next
	}
	return at, nil
}

func isDirFd(fd int) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	return st.Mode&unix.S_IFMT == unix.S_IFDIR, nil
}

func attach(srcFd, targetFd int, attrs uint64) error {
	mfd, err := unix.OpenTree(srcFd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_RECURSIVE|unix.AT_EMPTY_PATH)
	if err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	defer unix.Close(mfd)
	if attrs != 0 {
		if err := unix.MountSetattr(mfd, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: attrs}); err != nil {
			return fmt.Errorf("restrict: %w", err)
		}
	}
	if err := unix.MoveMount(mfd, "", targetFd, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	return nil
}

func bind(rootFd int, src, at string, attrs uint64, exact bool) error {
	sfd, err := openSource(src, exact)
	if err != nil {
		return err
	}
	defer unix.Close(sfd)
	dir, err := isDirFd(sfd)
	if err != nil {
		return fmt.Errorf("fence: %s: %w", src, err)
	}
	tfd, err := mountPoint(rootFd, at, dir)
	if err != nil {
		return err
	}
	defer unix.Close(tfd)
	if err := attach(sfd, tfd, attrs); err != nil {
		return fmt.Errorf("fence: bind %s: %w", src, err)
	}
	return nil
}

func hide(rootFd int, path string) error {
	fd, err := beneath(rootFd, strings.TrimPrefix(filepath.Clean(path), "/"), unix.O_PATH)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	defer unix.Close(fd)
	dir, err := isDirFd(fd)
	if err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	sealed := unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC
	if !dir {
		null, err := openSource("/dev/null", false)
		if err != nil {
			return err
		}
		defer unix.Close(null)
		if err := attach(null, fd, uint64(sealed)); err != nil {
			return fmt.Errorf("fence: hide %s: %w", path, err)
		}
		return nil
	}
	fsfd, err := unix.Fsopen("tmpfs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	defer unix.Close(fsfd)
	if err := unix.FsconfigSetString(fsfd, "mode", "0500"); err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	if err := unix.FsconfigCreate(fsfd); err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	mfd, err := unix.Fsmount(fsfd, unix.FSMOUNT_CLOEXEC, sealed)
	if err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	defer unix.Close(mfd)
	if err := unix.MoveMount(mfd, "", fd, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("fence: hide %s: %w", path, err)
	}
	return nil
}

func runLinks(root string) error {
	run := filepath.Join(root, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	entries, err := os.ReadDir("/run")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		dest, err := os.Readlink(filepath.Join("/run", e.Name()))
		if err != nil {
			continue
		}
		if err := os.Symlink(dest, filepath.Join(run, e.Name())); err != nil {
			return fmt.Errorf("fence: %w", err)
		}
	}
	return nil
}

func devices(root string) error {
	dev := filepath.Join(root, "dev")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := tmpfs(dev, "0755"); err != nil {
		return err
	}
	devFd, err := unix.Open(dev, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	defer unix.Close(devFd)
	for _, n := range devNodes {
		if err := bind(devFd, "/dev/"+n, n, unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NOEXEC, false); err != nil {
			return err
		}
	}
	pts := filepath.Join(dev, "pts")
	if err := os.MkdirAll(pts, 0o755); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := unix.Mount("devpts", pts, "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"); err != nil {
		return fmt.Errorf("fence: devpts: %w", err)
	}
	shm := filepath.Join(dev, "shm")
	if err := os.MkdirAll(shm, 0o755); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := tmpfs(shm, "1777"); err != nil {
		return err
	}
	for name, dest := range map[string]string{
		"ptmx": "pts/ptmx", "fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2",
	} {
		if err := os.Symlink(dest, filepath.Join(dev, name)); err != nil {
			return fmt.Errorf("fence: %w", err)
		}
	}
	return nil
}

func procfs(root string) error {
	p := filepath.Join(root, "proc")
	if err := os.MkdirAll(p, 0o555); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := unix.Mount("proc", p, "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("fence: proc: %w", err)
	}
	return nil
}

func scratchDirs(root string) error {
	for _, p := range []string{"tmp", "var/tmp"} {
		if err := mkdirIn(root, p); err != nil {
			return err
		}
		if err := tmpfs(filepath.Join(root, p), "1777"); err != nil {
			return err
		}
	}
	if err := os.Symlink("../run", filepath.Join(root, "var", "run")); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	return nil
}

func pivot(root string) error {
	if err := unix.Chdir(root); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("fence: pivot_root: %w", err)
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("fence: detach the old root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := unix.MountSetattr(unix.AT_FDCWD, "/", 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}); err != nil {
		return fmt.Errorf("fence: seal the root: %w", err)
	}
	return nil
}
