package gateway

// The load-bearing test: an UNMODIFIED jkrpc client, over a websocket, through
// the pump, against a real jkrpc server on a unix socket. If calls round-trip
// and notifications arrive, the tunnel preserves the contract -- which is the
// entire claim §2 of plans/http-gateway.md makes.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jack-work/jkrpc"
)

// echoDaemon stands in for the angelus: a jkrpc server on a unix socket with
// one method that echoes, and one that pushes a notification back.
func echoDaemon(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				conn := jkrpc.NewConn(c)
				var srv *jkrpc.Server
				srv = jkrpc.NewServer(conn, map[string]jkrpc.HandlerFunc{
					"echo": func(ctx context.Context, p json.RawMessage) (any, error) {
						return map[string]any{"got": json.RawMessage(p)}, nil
					},
					"push": func(ctx context.Context, p json.RawMessage) (any, error) {
						// Three notifications, then the response: the order a
						// streaming turn actually produces.
						for i := 0; i < 3; i++ {
							_ = srv.Notify("frame", map[string]int{"n": i})
						}
						return map[string]bool{"ok": true}, nil
					},
				})
				_ = srv.Serve(context.Background())
			}(c)
		}
	}()
	return sock
}

// dialTunnel is what the CLI's https transport will do, in miniature.
func dialTunnel(t *testing.T, url string, onNotify jkrpc.NotifyFunc) *jkrpc.Client {
	t.Helper()
	ws, _, err := websocket.Dial(context.Background(),
		strings.Replace(url, "http", "ws", 1)+"/v1/socket", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	ws.SetReadLimit(-1)
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	return jkrpc.NewClient(jkrpc.NewConn(nc), onNotify)
}

func TestTunnelCarriesCallsAndNotifications(t *testing.T) {
	sock := echoDaemon(t)
	srv := httptest.NewServer(&Tunnel{Dial: UnixDialer(sock)})
	defer srv.Close()

	var mu sync.Mutex
	var frames []string
	done := make(chan struct{})
	cli := dialTunnel(t, srv.URL, func(method string, p json.RawMessage) {
		mu.Lock()
		frames = append(frames, method)
		n := len(frames)
		mu.Unlock()
		if n == 3 {
			close(done)
		}
	})
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. A call round-trips with its params intact.
	var got struct {
		Got struct {
			Hello string `json:"hello"`
		} `json:"got"`
	}
	if err := cli.Call(ctx, "echo", map[string]string{"hello": "figaro"}, &got); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if got.Got.Hello != "figaro" {
		t.Fatalf("params mangled: %+v", got)
	}

	// 2. Server-pushed notifications arrive, in order, on the same connection.
	var ok struct {
		OK bool `json:"ok"`
	}
	if err := cli.Call(ctx, "push", nil, &ok); err != nil {
		t.Fatalf("push: %v", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		mu.Lock()
		t.Fatalf("notifications never arrived: got %v", frames)
	}

	// 3. Many calls on one tunnel: no framing drift as the stream goes on.
	for i := 0; i < 50; i++ {
		var r struct {
			Got struct {
				N int `json:"n"`
			} `json:"got"`
		}
		if err := cli.Call(ctx, "echo", map[string]int{"n": i}, &r); err != nil {
			t.Fatalf("echo %d: %v", i, err)
		}
		if r.Got.N != i {
			t.Fatalf("call %d answered with %d: responses are crossing", i, r.Got.N)
		}
	}
}

// A frame larger than one bufio buffer must survive the filter's line
// reader intact -- this is the case ReadSlice's ErrBufferFull loop exists for,
// and getting it wrong corrupts exactly the big tool outputs that matter.
func TestTunnelLargeFrameThroughFilter(t *testing.T) {
	sock := echoDaemon(t)
	srv := httptest.NewServer(&Tunnel{
		Dial:   UnixDialer(sock),
		Filter: &Filter{Check: func(string) Decision { return Decision{Allow: true} }},
	})
	defer srv.Close()

	cli := dialTunnel(t, srv.URL, nil)
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	big := strings.Repeat("x", 512<<10) // 512 KiB, well past the 64 KiB reader
	var got struct {
		Got struct {
			Blob string `json:"blob"`
		} `json:"got"`
	}
	if err := cli.Call(ctx, "echo", map[string]string{"blob": big}, &got); err != nil {
		t.Fatalf("big echo: %v", err)
	}
	if got.Got.Blob != big {
		t.Fatalf("large frame corrupted: got %d bytes, want %d", len(got.Got.Blob), len(big))
	}
}

func TestFilterRefusesMethodAndAnswersCaller(t *testing.T) {
	sock := echoDaemon(t)
	srv := httptest.NewServer(&Tunnel{
		Dial: UnixDialer(sock),
		Filter: &Filter{Check: func(m string) Decision {
			if m == "push" {
				return Decision{Allow: false, Reason: "agency withheld"}
			}
			return Decision{Allow: true}
		}},
	})
	defer srv.Close()

	cli := dialTunnel(t, srv.URL, nil)
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The refused call gets an ERROR, not a hang: the caller must learn its
	// verdict, and a filter that silently dropped frames would deadlock it.
	err := cli.Call(ctx, "push", nil, nil)
	if err == nil {
		t.Fatal("refused method returned no error")
	}
	var jerr *jkrpc.Error
	if !asJKError(err, &jerr) {
		t.Fatalf("want a jsonrpc error, got %T: %v", err, err)
	}
	if jerr.Code != refusalCode {
		t.Fatalf("code = %d, want %d", jerr.Code, refusalCode)
	}
	if !strings.Contains(jerr.Message, "agency withheld") {
		t.Fatalf("reason not carried: %q", jerr.Message)
	}

	// And the connection SURVIVES the refusal: a denied call must not sever
	// the tunnel, or one mistake costs the client every in-flight call.
	var got struct{ Got json.RawMessage }
	if err := cli.Call(ctx, "echo", map[string]int{"n": 1}, &got); err != nil {
		t.Fatalf("connection died after refusal: %v", err)
	}
}

func asJKError(err error, out **jkrpc.Error) bool {
	e, ok := err.(*jkrpc.Error)
	if ok {
		*out = e
	}
	return ok
}

func TestTunnelRefusesBrowserOriginByDefault(t *testing.T) {
	sock := echoDaemon(t)
	srv := httptest.NewServer(&Tunnel{Dial: UnixDialer(sock)})
	defer srv.Close()

	// A page on evil.example trying to open a tunnel presents an Origin.
	// With no Origins configured this must fail the handshake.
	_, _, err := websocket.Dial(context.Background(),
		strings.Replace(srv.URL, "http", "ws", 1)+"/v1/socket",
		&websocket.DialOptions{HTTPHeader: http.Header{
			"Origin": []string{"https://evil.example"},
		}})
	if err == nil {
		t.Fatal("a cross-origin browser tunnel was accepted")
	}
}
