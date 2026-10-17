//go:build !linux

package authz

import (
	"errors"
	"net"

	"github.com/jack-work/figaro/api/rpc"
)

func peer(net.Conn) (rpc.Principal, error) { return rpc.Principal{}, nil }

func PeerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peercred: unsupported on this platform")
}
