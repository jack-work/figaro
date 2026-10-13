package figaro

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/sdk"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/jkrpc"
)

// Hop two: angelus -> agent, over a real socket so the seam between the
// wrapper and jkrpc's marshaling is the one under test.
func TestAgentClientPresentsCallerAcrossTheSecondHop(t *testing.T) {
	t.Setenv("FIGARO_ARIA", "caller02")

	// sun_path is length-capped; keep the directory short.
	dir, err := os.MkdirTemp("", "f")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "a.sock")

	seen := make(chan json.RawMessage, 8)
	record := func(_ context.Context, params json.RawMessage) (any, error) {
		seen <- append(json.RawMessage(nil), params...)
		return map[string]any{}, nil
	}
	handlers := map[string]jkrpc.HandlerFunc{
		rpc.MethodSet:  record,
		rpc.MethodForm: record,
	}
	ln, err := transport.Listen(transport.UnixEndpoint(sock))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go jkrpc.NewServer(jkrpc.NewConn(conn), handlers).Serve(context.Background())
		}
	}()

	cli, err := sdk.DialAria(transport.UnixEndpoint(sock), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	next := func() json.RawMessage {
		t.Helper()
		select {
		case p := <-seen:
			return p
		case <-time.After(5 * time.Second):
			t.Fatal("agent received no request")
			return nil
		}
	}

	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"keep me exact"`),
	}, nil)
	if _, err := cli.Set(context.Background(), patch, 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := next()
	if id := rpc.CallerOf(got); id != "caller02" {
		t.Fatalf("set caller = %q, want caller02 (params: %s)", id, got)
	}
	var sr rpc.SetRequest
	if err := json.Unmarshal(got, &sr); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if v := string(mustEntry(sr.Patch, "mantra")); v != `"keep me exact"` {
		t.Fatalf("patch value re-encoded: %s", v)
	}

	if _, err := cli.Form(context.Background()); err != nil {
		t.Fatalf("form: %v", err)
	}
	if id := rpc.CallerOf(next()); id != "caller02" {
		t.Fatalf("nil-params caller = %q, want caller02", id)
	}
}

func mustEntry(p form.Patch, key string) json.RawMessage {
	e, ok := p.Entry(key)
	if !ok {
		return nil
	}
	return e.New
}
