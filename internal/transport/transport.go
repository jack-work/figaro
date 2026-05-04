// Package transport defines the connection abstraction between figaro components.
//
// All inter-component communication is JSON-RPC 2.0 over NDJSON
// (newline-delimited JSON). The jsonrpc.Conn wraps any net.Conn
// with ordered read/write.
//
// An Endpoint is a serializable descriptor that tells a client how to
// connect. It uses URI syntax with the scheme as the transport discriminator:
//
//	unix:///run/user/1000/figaro/figaros/abc.sock
//	tcp://192.168.1.5:9090
//
// Currently only "unix" and "tcp" are implemented.
package transport

import (
	"fmt"
	"net"

	"github.com/jack-work/figaro/internal/jsonrpc"
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

// Dial connects to an endpoint and returns a jsonrpc.Conn.
func Dial(ep Endpoint) (*jsonrpc.Conn, error) {
	conn, err := DialRaw(ep)
	if err != nil {
		return nil, err
	}
	return jsonrpc.NewConn(conn), nil
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
