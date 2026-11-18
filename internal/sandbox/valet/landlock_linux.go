//go:build linux

package valet

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	llFileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE |
		unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

	llRead = unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR

	llWriteV1 = llRead |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM

	llHandledV1 = llWriteV1 |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK
)

func LandlockABI() int {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(abi)
}

func (f Fence) Lock() error {
	if _, _, errno := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0, 0); errno != 0 {
		if errno != unix.ENOTSUP {
			return fmt.Errorf("fence: no_new_privs: %w", errno)
		}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("fence: no_new_privs: %w", err)
		}
	}
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno == unix.ENOSYS || errno == unix.EOPNOTSUPP {
		return nil
	}
	if errno != 0 {
		return fmt.Errorf("fence: landlock version: %w", errno)
	}
	rw := uint64(llWriteV1)
	handled := uint64(llHandledV1)
	dev := uint64(0)
	scoped := uint64(0)
	if abi >= 2 {
		rw |= unix.LANDLOCK_ACCESS_FS_REFER
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		rw |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		dev = unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
		handled |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	if abi >= 6 {
		scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	}
	attr := unix.LandlockRulesetAttr{
		Access_fs: handled,
		Scoped:    scoped,
	}
	rfd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("fence: landlock create ruleset: %w", errno)
	}
	defer unix.Close(int(rfd))

	if err := landlockAllow(int(rfd), "/", unix.LANDLOCK_ACCESS_FS_READ_DIR, true); err != nil {
		return err
	}
	for _, p := range append(append([]string{}, systemTrees...), "/proc", "/run") {
		if err := landlockAllow(int(rfd), p, llRead, true); err != nil {
			return err
		}
	}
	if err := landlockAllow(int(rfd), "/dev", rw|dev, true); err != nil {
		return err
	}
	for _, p := range []string{"/tmp", "/var/tmp", f.Scratch} {
		if p != "" {
			if err := landlockAllow(int(rfd), p, rw, true); err != nil {
				return err
			}
		}
	}
	if f.Door != "" {
		if err := landlockAllow(int(rfd), f.Door, llRead|unix.LANDLOCK_ACCESS_FS_WRITE_FILE, true); err != nil {
			return err
		}
	}
	for _, g := range f.ordered() {
		rights := uint64(llRead)
		if g.Write {
			rights = rw
		}
		if err := landlockAllow(int(rfd), g.Path, rights, false); err != nil {
			return err
		}
	}
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rfd, 0, 0); errno != 0 {
		if errno != unix.ENOTSUP {
			return fmt.Errorf("fence: landlock restrict: %w", errno)
		}
		if _, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rfd, unix.LANDLOCK_RESTRICT_SELF_TSYNC, 0); errno != 0 {
			if _, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rfd, 0, 0); errno != 0 {
				return fmt.Errorf("fence: landlock restrict: %w", errno)
			}
		}
	}
	return nil
}

func landlockAllow(rfd int, path string, access uint64, optional bool) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if optional && errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("fence: landlock open %s: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("fence: landlock stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= llFileRights
	}
	if access == 0 {
		return nil
	}
	rule := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(fd),
	}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rfd), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("fence: landlock allow %s: %w", path, errno)
	}
	return nil
}
