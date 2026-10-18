//go:build linux

package authz

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/jack-work/figaro/api/rpc"
	"golang.org/x/sys/unix"
)

func peer(conn net.Conn) (rpc.Principal, error) {
	uid, err := PeerUID(conn)
	if err != nil {
		return rpc.Principal{}, err
	}
	if int(uid) != os.Getuid() {
		return rpc.Principal{}, fmt.Errorf("peer uid %d, want %d", uid, os.Getuid())
	}
	return rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(int(uid))}, nil
}

func PeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("%w: peercred vouches only for a unix socket, not %T", ErrUnadmitted, conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	if cerr := raw.Control(func(fd uintptr) {
		cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil {
		return 0, cerr
	}
	if err != nil {
		return 0, err
	}
	return cred.Uid, nil
}
