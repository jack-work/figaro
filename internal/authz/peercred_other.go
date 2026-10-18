//go:build !linux

package authz

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/jack-work/figaro/api/rpc"
)

func peer(conn net.Conn) (rpc.Principal, error) {
	uid, err := PeerUID(conn)
	if err != nil {
		return rpc.Principal{}, err
	}
	return rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.FormatUint(uint64(uid), 10)}, nil
}

func PeerUID(conn net.Conn) (uint32, error) {
	if _, ok := conn.(*net.UnixConn); !ok {
		return 0, fmt.Errorf("%w: peercred vouches only for a unix socket, not %T", ErrUnadmitted, conn)
	}
	return uint32(os.Getuid()), nil
}
