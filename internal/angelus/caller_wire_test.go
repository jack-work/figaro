package angelus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/jkrpc"
)

// recordingServer accepts one connection on a unix socket and records the raw
// params of every request it receives.
//
// This goes over a REAL socket rather than a stubbed client because the thing
// under test is the seam between our typed wrapper and jkrpc's marshaling —
// exactly the seam a mock would paper over. It also means a client that
// recurses instead of dialing fails here loudly (stack overflow) rather than
// in production on the first RPC.
type recordingServer struct {
	sock   string
	params chan json.RawMessage
}

func startRecordingServer(t *testing.T, methods ...string) *recordingServer {
	t.Helper()
	// Scratch on /var/tmp: /tmp is tmpfs here, and a unix socket path is
	// length-capped (sun_path), so keep the directory short.
	dir, err := os.MkdirTemp("/var/tmp", "figauthz")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	rs := &recordingServer{sock: filepath.Join(dir, "s.sock"), params: make(chan json.RawMessage, 8)}
	handlers := map[string]jkrpc.HandlerFunc{}
	for _, m := range methods {
		handlers[m] = func(_ context.Context, params json.RawMessage) (any, error) {
			rs.params <- append(json.RawMessage(nil), params...)
			return map[string]any{}, nil
		}
	}
	ln, err := transport.Listen(transport.UnixEndpoint(rs.sock))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveOne(conn, handlers)
		}
	}()
	return rs
}

func serveOne(conn net.Conn, handlers map[string]jkrpc.HandlerFunc) {
	srv := jkrpc.NewServer(jkrpc.NewConn(conn), handlers)
	srv.Serve(context.Background())
}

func (rs *recordingServer) next(t *testing.T) json.RawMessage {
	t.Helper()
	select {
	case p := <-rs.params:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("server received no request")
		return nil
	}
}

// Hop one: CLI -> angelus. The identity must arrive on a method with a payload
// AND on one that sends nil params.
func TestAngelusClientPresentsCallerOnBothParamShapes(t *testing.T) {
	t.Setenv("FIGARO_ARIA", "caller01")
	rs := startRecordingServer(t, rpc.MethodFork, rpc.MethodSaveBindings)

	// caller is captured at dial, so the env has to be set first.
	cli, err := DialClient(transport.UnixEndpoint(rs.sock))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	ctx := context.Background()

	if _, err := cli.Fork(ctx, "target07", 3, 0); err != nil {
		t.Fatalf("fork: %v", err)
	}
	got := rs.next(t)
	if id := rpc.CallerOf(got); id != "caller01" {
		t.Fatalf("fork caller = %q, want caller01 (params: %s)", id, got)
	}
	// The payload must still be intact beside the credential.
	var fr rpc.ForkRequest
	if err := json.Unmarshal(got, &fr); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if fr.FigaroID != "target07" || fr.AtTurn != 3 {
		t.Fatalf("payload mangled: %+v (params: %s)", fr, got)
	}

	// SaveBindings passes nil params — the shape that cannot carry an embedded
	// struct, and so the shape most likely to lose the identity.
	if _, err := cli.SaveBindings(ctx); err != nil {
		t.Fatalf("saveBindings: %v", err)
	}
	if id := rpc.CallerOf(rs.next(t)); id != "caller01" {
		t.Fatalf("nil-params caller = %q, want caller01", id)
	}
}

// A human at a terminal has no aria identity. The wire must look exactly as it
// did before this change: no extra field.
func TestAngelusClientOmitsCallerWhenUnset(t *testing.T) {
	t.Setenv("FIGARO_ARIA", "")
	rs := startRecordingServer(t, rpc.MethodFork)
	cli, err := DialClient(transport.UnixEndpoint(rs.sock))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Fork(context.Background(), "target07", 0, 0); err != nil {
		t.Fatalf("fork: %v", err)
	}
	got := rs.next(t)
	if id := rpc.CallerOf(got); id != "" {
		t.Fatalf("caller = %q, want empty", id)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatalf("params: %v", err)
	}
	if _, present := fields[rpc.CallerKey]; present {
		t.Fatalf("%s present with no identity: %s", rpc.CallerKey, got)
	}
}
