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
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return rpc.Principal{}, nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return rpc.Principal{}, err
	}
	var cred *unix.Ucred
	if cerr := raw.Control(func(fd uintptr) {
		cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil {
		err = cerr
	}
	if err != nil {
		return rpc.Principal{}, err
	}
	if int(cred.Uid) != os.Getuid() {
		return rpc.Principal{}, fmt.Errorf("peer uid %d, want %d", cred.Uid, os.Getuid())
	}
	return rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(int(cred.Uid))}, nil
}
