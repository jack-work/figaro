//go:build !linux

package authz

import (
	"net"

	"github.com/jack-work/figaro/api/rpc"
)

func peer(net.Conn) (rpc.Principal, error) { return rpc.Principal{}, nil }
