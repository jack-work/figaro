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

	ln, err := transport.Listen(ep)
	require.NoError(t, err)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	conn, err := transport.Dial(ep)
	require.NoError(t, err)

	serverConn := <-accepted
	require.NotNil(t, serverConn)
	serverConn.Close()
	conn.Close()
}

func TestDial_UnsupportedScheme(t *testing.T) {
	ep := transport.Endpoint{Scheme: "smoke-signal", Address: "mountain-top"}
	_, err := transport.Dial(ep)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport scheme")
}

func TestListen_CreatesSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "sub", "test.sock")
	_, err := transport.Listen(transport.UnixEndpoint(sockPath))
	assert.Error(t, err)

	require.NoError(t, os.MkdirAll(filepath.Dir(sockPath), 0700))
	ln, err := transport.Listen(transport.UnixEndpoint(sockPath))
	require.NoError(t, err)
	ln.Close()
}
