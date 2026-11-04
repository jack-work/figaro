//go:build linux

package cli

import (
	"net"

	"golang.org/x/sys/unix"
)

func socketOwnerPID(path string) (int, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		cred, gerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, gerr
	}
	return int(cred.Pid), nil
}
