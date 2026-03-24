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
	"net/url"

	"github.com/jack-work/figaro/internal/jsonrpc"
)

// Endpoint describes how to reach a figaro component (angelus or figaro agent).
type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
}

// URI returns the endpoint as a URI string.
func (e Endpoint) URI() string {
	switch e.Scheme {
	case "unix":
		return "unix://" + e.Address
	case "tcp":
		return "tcp://" + e.Address
	default:
		return e.Scheme + "://" + e.Address
	}
}

// String implements fmt.Stringer.
func (e Endpoint) String() string {
	return e.URI()
}

// ParseEndpoint parses a URI string into an Endpoint.
func ParseEndpoint(uri string) (Endpoint, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return Endpoint{}, fmt.Errorf("parse endpoint %q: %w", uri, err)
	}
	switch u.Scheme {
	case "unix":
		return Endpoint{Scheme: "unix", Address: u.Path}, nil
	case "tcp":
		return Endpoint{Scheme: "tcp", Address: u.Host}, nil
	default:
		return Endpoint{Scheme: u.Scheme, Address: u.Host + u.Path}, nil
	}
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
