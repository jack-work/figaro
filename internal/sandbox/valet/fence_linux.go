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
	for _, p := range systemTrees {
		if _, err := os.Stat(p); err == nil {
			if err := bind(p, filepath.Join(root, p), unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV); err != nil {
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
		if err := bind(g.Path, filepath.Join(root, g.Path), attrs); err != nil {
			return err
		}
	}
	for _, p := range f.Hide {
		if err := hide(filepath.Join(root, p)); err != nil {
			return err
		}
	}
	if f.Scratch != "" {
		if err := bind(f.Scratch, filepath.Join(root, f.Scratch), unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV); err != nil {
			return err
		}
	}
	if f.Door != "" {
		if err := bind(f.Door, filepath.Join(root, f.Door), unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV|unix.MOUNT_ATTR_NOEXEC); err != nil {
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

func bind(src, target string, attrs uint64) error {
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if fi.IsDir() {
		err = os.MkdirAll(target, 0o755)
	} else if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
		var f *os.File
		if f, err = os.OpenFile(target, os.O_CREATE|os.O_RDONLY, 0o644); err == nil {
			f.Close()
		}
	}
	if err != nil {
		return fmt.Errorf("fence: mount point for %s: %w", src, err)
	}
	if err := unix.Mount(src, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("fence: bind %s: %w", src, err)
	}
	if err := unix.MountSetattr(unix.AT_FDCWD, target, unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: attrs}); err != nil {
		return fmt.Errorf("fence: restrict %s: %w", src, err)
	}
	return nil
}

func hide(target string) error {
	fi, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if fi.IsDir() {
		if err := tmpfs(target, "0500"); err != nil {
			return err
		}
		if err := unix.MountSetattr(unix.AT_FDCWD, target, 0, &unix.MountAttr{
			Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC,
		}); err != nil {
			return fmt.Errorf("fence: seal hidden %s: %w", target, err)
		}
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return bind("/dev/null", target, unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NOEXEC)
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
	for _, n := range devNodes {
		if err := bind("/dev/"+n, filepath.Join(dev, n), unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NOEXEC); err != nil {
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
