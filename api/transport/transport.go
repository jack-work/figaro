// Package transport defines connection abstractions between components.
// Endpoints use URI syntax (unix://, tcp://, or https:// for a remote
// daemon reached through the HTTP gateway).
package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/jack-work/jkrpc"
)

// Endpoint describes how to reach a figaro component (angelus or figaro agent).
type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
	// Bearer is the credential presented when Scheme is http(s). It is
	// carried on the Endpoint rather than looked up at dial time so that
	// exactly one place -- the CLI's origin resolver -- decides which secret
	// belongs to which host, and nothing below this line reads a keyring.
	//
	// It is never serialized: a credential has no business in the JSON an
	// endpoint travels as (attach responses hand endpoints back to clients).
	Bearer string `json:"-"`
}

// Remote reports whether this endpoint leaves the machine. A remote daemon is
// never auto-started, and pid bindings are meaningless against one.
func (e Endpoint) Remote() bool {
	return e.Scheme == "http" || e.Scheme == "https"
}

// HTTPEndpoint constructs a gateway endpoint from a base URL.
func HTTPEndpoint(rawURL, bearer string) (Endpoint, error) {
	u := strings.TrimRight(rawURL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		return Endpoint{Scheme: "https", Address: strings.TrimPrefix(u, "https://"), Bearer: bearer}, nil
	case strings.HasPrefix(u, "http://"):
		return Endpoint{Scheme: "http", Address: strings.TrimPrefix(u, "http://"), Bearer: bearer}, nil
	default:
		return Endpoint{}, fmt.Errorf("origin %q: want an http:// or https:// URL", rawURL)
	}
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
	case "http", "https":
		return dialGateway(ep)
	default:
		return nil, fmt.Errorf("unsupported transport scheme %q", ep.Scheme)
	}
}

// gatewayHandshake bounds the upgrade, not the session. Once the tunnel is
// open it lives as long as the caller wants: a `listen` may hold it for hours.
const gatewayHandshake = 30 * time.Second

// dialGateway opens the tunnel: a WebSocket to /v1/socket, presented as a
// net.Conn so jkrpc sees exactly the stream it always saw. The gateway on
// the far side copies these bytes to the angelus socket without parsing
// them, so every method -- including ones added after this code was written
// -- works across it.
func dialGateway(ep Endpoint) (net.Conn, error) {
	ws := "wss"
	if ep.Scheme == "http" {
		ws = "ws"
	}
	hdr := http.Header{}
	if ep.Bearer != "" {
		hdr.Set("Authorization", "Bearer "+ep.Bearer)
	}
	opts := &websocket.DialOptions{HTTPHeader: hdr}

	// A gateway on a unix socket is addressed "unix:/path/to.sock". There is
	// no host to put in a URL, so we keep a placeholder authority (which the
	// server ignores) and teach the HTTP client to dial the socket instead.
	// This is the local shape: `figaro serve` binds a unix socket, and a
	// reverse proxy -- or a test -- reaches it without a port existing.
	addr := ep.Address
	if sock, ok := strings.CutPrefix(addr, "unix:"); ok {
		opts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
			},
		}
		ws, addr = "ws", "figaro.local"
	}

	// The handshake gets a deadline; the resulting conn does not inherit it,
	// which is why the context is detached below.
	ctx, cancel := context.WithTimeout(context.Background(), gatewayHandshake)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, ws+"://"+addr+"/v1/socket", opts)
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, fmt.Errorf("gateway %s refused this credential (%s)",
					ep.Address, resp.Status)
			case http.StatusNotFound:
				return nil, fmt.Errorf("gateway %s has no tunnel at /v1/socket (%s): is that a figaro gateway?",
					ep.Address, resp.Status)
			}
			return nil, fmt.Errorf("dial gateway %s: %s: %w", ep.Address, resp.Status, err)
		}
		return nil, fmt.Errorf("dial gateway %s: %w", ep.Address, err)
	}
	// No read limit, for the same reason the gateway sets none: a page
	// carrying a large tool output is a legitimate frame.
	c.SetReadLimit(-1)
	// context.Background, deliberately: the conn outlives the handshake.
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
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
