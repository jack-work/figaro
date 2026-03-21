// Package transport defines the connection abstraction between figaro components.
//
// All inter-component communication is JSON-RPC 2.0 over a channel.Channel
// (from creachadair/jrpc2). The Channel interface handles framing and is
// transport-agnostic — unix sockets, TCP, websockets, and in-memory pipes
// all satisfy it.
//
// An Endpoint is a serializable descriptor that tells a client how to
// connect. It uses URI syntax with the scheme as the transport discriminator:
//
//	unix:///run/user/1000/figaro/figaros/abc.sock
//	tcp://192.168.1.5:9090
//	ws://host:8080/figaro/abc
//
// Currently only "unix" is implemented. The Dial function takes an Endpoint
// and returns a channel.Channel ready for jrpc2.NewClient or jrpc2.NewServer.
package transport

import (
	"fmt"
	"net"
	"net/url"

	"github.com/creachadair/jrpc2/channel"
)

// Endpoint describes how to reach a figaro component (angelus or figaro agent).
// Serialized as a URI string in JSON-RPC responses.
type Endpoint struct {
	// Scheme is the transport discriminator: "unix", "tcp", "ws", etc.
	Scheme string `json:"scheme"`

	// Address is scheme-specific. For "unix" it's the socket path.
	// For "tcp" it's "host:port". For "ws" it's the full URL.
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
		// unix:///path/to/sock → Path is "/path/to/sock"
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

// Dial connects to an endpoint and returns a jrpc2 Channel.
// The Channel uses line-framed JSON (newline-delimited), which is
// the simplest framing for unix sockets and TCP.
func Dial(ep Endpoint) (channel.Channel, error) {
	conn, err := DialConn(ep)
	if err != nil {
		return nil, err
	}
	return channel.Line(conn, conn), nil
}

// DialConn connects to an endpoint and returns the raw net.Conn.
// Use Dial for the common case (returns a Channel directly).
func DialConn(ep Endpoint) (net.Conn, error) {
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
// Used by the angelus and figaro to accept connections.
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

// WrapConn wraps a net.Conn into a jrpc2 Channel using line framing.
func WrapConn(conn net.Conn) channel.Channel {
	return channel.Line(conn, conn)
}
