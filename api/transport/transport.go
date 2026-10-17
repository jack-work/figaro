// Package transport dials and listens on unix and tcp endpoints.
package transport

import (
	"fmt"
	"net"
	"net/http"

	"github.com/jack-work/jkrpc"
)

type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
	Bearer  Bearer `json:"-"`

	HTTPClient *http.Client `json:"-"`
}

type Bearer interface {
	Resolve() (string, error)
	Invalidate(token string) error
}

func UnixEndpoint(path string) Endpoint {
	return Endpoint{Scheme: "unix", Address: path}
}

func WSSEndpoint(url string, bearer Bearer) Endpoint {
	return Endpoint{Scheme: "wss", Address: url, Bearer: bearer}
}

// Tap wraps the raw conn before the RPC layer uses it; nil is the identity.
type Tap func(net.Conn) net.Conn

func Dial(ep Endpoint) (*jkrpc.Conn, error) { return DialWith(ep, nil) }

func DialWith(ep Endpoint, tap Tap) (*jkrpc.Conn, error) {
	conn, err := DialRaw(ep)
	if err != nil {
		return nil, err
	}
	if tap != nil {
		conn = tap(conn)
	}
	return jkrpc.NewConn(conn), nil
}

func DialRaw(ep Endpoint) (net.Conn, error) {
	switch ep.Scheme {
	case "unix":
		return net.Dial("unix", ep.Address)
	case "tcp":
		return net.Dial("tcp", ep.Address)
	case "wss":
		return dialWSS(ep)
	default:
		return nil, fmt.Errorf("unsupported transport scheme %q", ep.Scheme)
	}
}

func Listen(ep Endpoint) (net.Listener, error) {
	switch ep.Scheme {
	case "unix":
		return net.Listen("unix", ep.Address)
	case "tcp":
		return net.Listen("tcp", ep.Address)
	default:
		return nil, fmt.Errorf("unsupported transport scheme %q", ep.Scheme)
	}
}
