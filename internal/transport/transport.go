// Package transport defines connection abstractions between components.
// Endpoints use URI syntax (unix:// or tcp://).
package transport

import (
	"fmt"
	"net"

	"github.com/jack-work/jkrpc"
)

// Endpoint describes how to reach a figaro component (angelus or figaro agent).
type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
}

// UnixEndpoint is a convenience constructor for unix socket endpoints.
func UnixEndpoint(path string) Endpoint {
	return Endpoint{Scheme: "unix", Address: path}
}

// Tap is middleware over a live connection: it is handed the raw conn and
// returns the conn the RPC layer will actually use. A nil Tap is the identity,
// which is what every caller but the recorder passes.
type Tap func(net.Conn) net.Conn

// Dial connects to an endpoint and returns a jkrpc.Conn.
func Dial(ep Endpoint) (*jkrpc.Conn, error) { return DialWith(ep, nil) }

// DialWith is Dial with connection middleware.
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

// DialRaw connects to an endpoint and returns the raw net.Conn.
func DialRaw(ep Endpoint) (net.Conn, error) {
	switch ep.Scheme {
	case "unix":
		return net.Dial("unix", ep.Address)
	case "tcp":
		return net.Dial("tcp", ep.Address)
	default:
		return nil, fmt.Errorf("unsupported transport scheme %q", ep.Scheme)
	}
}

// Listen creates a net.Listener for an endpoint.
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
