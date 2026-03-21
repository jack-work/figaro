package transport_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/transport"
)

func TestEndpoint_URI(t *testing.T) {
	tests := []struct {
		name string
		ep   transport.Endpoint
		want string
	}{
		{"unix", transport.Endpoint{Scheme: "unix", Address: "/tmp/test.sock"}, "unix:///tmp/test.sock"},
		{"tcp", transport.Endpoint{Scheme: "tcp", Address: "localhost:9090"}, "tcp://localhost:9090"},
		{"custom", transport.Endpoint{Scheme: "ws", Address: "host:8080/path"}, "ws://host:8080/path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.ep.URI())
			assert.Equal(t, tt.want, tt.ep.String())
		})
	}
}

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		uri    string
		scheme string
		addr   string
	}{
		{"unix:///tmp/test.sock", "unix", "/tmp/test.sock"},
		{"tcp://localhost:9090", "tcp", "localhost:9090"},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			ep, err := transport.ParseEndpoint(tt.uri)
			require.NoError(t, err)
			assert.Equal(t, tt.scheme, ep.Scheme)
			assert.Equal(t, tt.addr, ep.Address)
		})
	}
}

func TestParseEndpoint_RoundTrip(t *testing.T) {
	ep := transport.UnixEndpoint("/run/user/1000/figaro/angelus.sock")
	parsed, err := transport.ParseEndpoint(ep.URI())
	require.NoError(t, err)
	assert.Equal(t, ep, parsed)
}

func TestUnixEndpoint(t *testing.T) {
	ep := transport.UnixEndpoint("/tmp/test.sock")
	assert.Equal(t, "unix", ep.Scheme)
	assert.Equal(t, "/tmp/test.sock", ep.Address)
}

func TestDialAndListen_Unix(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	ep := transport.UnixEndpoint(sockPath)

	// Listen.
	ln, err := transport.Listen(ep)
	require.NoError(t, err)
	defer ln.Close()

	// Accept in background.
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	// Dial.
	ch, err := transport.Dial(ep)
	require.NoError(t, err)

	// Should have connected.
	serverConn := <-accepted
	require.NotNil(t, serverConn)
	serverConn.Close()
	ch.Close()
}

func TestDial_UnsupportedScheme(t *testing.T) {
	ep := transport.Endpoint{Scheme: "smoke-signal", Address: "mountain-top"}
	_, err := transport.Dial(ep)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport scheme")
}

func TestWrapConn(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "wrap.sock")

	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		ch := transport.WrapConn(conn)
		// Echo back what we receive.
		msg, _ := ch.Recv()
		ch.Send(msg)
		ch.Close()
	}()

	conn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	ch := transport.WrapConn(conn)

	require.NoError(t, ch.Send([]byte(`{"hello":"world"}`)))
	reply, err := ch.Recv()
	require.NoError(t, err)
	assert.JSONEq(t, `{"hello":"world"}`, string(reply))
	ch.Close()
}

func TestListen_CreatesSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "sub", "test.sock")
	// sub/ doesn't exist yet — Listen should fail (we don't auto-create dirs).
	_, err := transport.Listen(transport.UnixEndpoint(sockPath))
	assert.Error(t, err)

	// Create the dir, then listen should work.
	require.NoError(t, os.MkdirAll(filepath.Dir(sockPath), 0700))
	ln, err := transport.Listen(transport.UnixEndpoint(sockPath))
	require.NoError(t, err)
	ln.Close()
}
