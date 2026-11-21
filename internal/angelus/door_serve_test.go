package angelus

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

// One connection carries a pager's reads and its queued prompts. A request
// that takes seconds (a cold page of a large figaro) must not hold back the
// next one on the same connection.
func TestASlowRequestDoesNotHoldBackTheNextOnItsConnection(t *testing.T) {
	release, entered := make(chan struct{}), make(chan struct{})
	defer close(release)
	a := &Angelus{Handlers: map[string]jkrpc.HandlerFunc{
		"slow": func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			close(entered)
			<-release
			return "slow", nil
		},
		"fast": func(context.Context, json.RawMessage) (interface{}, error) {
			return "fast", nil
		},
	}}
	ln, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		server, err := ln.Accept()
		if err == nil {
			a.handleConn(ctx, authz.DoorLocal, server)
		}
	}()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	cli := jkrpc.NewClient(jkrpc.NewConn(client), nil)
	defer cli.Close()
	go func() { _ = cli.Call(context.Background(), "slow", nil, new(string)) }()
	<-entered

	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	var got string
	if err := cli.Call(callCtx, "fast", nil, &got); err != nil {
		t.Fatalf("fast request behind a slow one on the same connection: %v", err)
	}
	if got != "fast" {
		t.Fatalf("got %q", got)
	}
}

// A request whose id is not an integer gets an invalid-request error, and the
// connection keeps serving: the gateway's handover lost whole connections to
// one string id.
func TestARequestWithAStringIDIsRefusedAndTheConnectionKeepsServing(t *testing.T) {
	a := &Angelus{Handlers: map[string]jkrpc.HandlerFunc{
		"fast": func(context.Context, json.RawMessage) (interface{}, error) { return "fast", nil },
	}}
	ln, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		server, err := ln.Accept()
		if err == nil {
			a.handleConn(ctx, authz.DoorLocal, server)
		}
	}()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":"gw-1","method":"fast"}` + "\n" + `{"jsonrpc":"2.0","id":7,"method":"fast"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(client)
	var refused, answered map[string]any
	if err := dec.Decode(&refused); err != nil {
		t.Fatalf("no answer to the string id: %v", err)
	}
	if e, _ := refused["error"].(map[string]any); e == nil || e["code"] != float64(-32600) {
		t.Fatalf("string id: want -32600, got %v", refused)
	}
	if err := dec.Decode(&answered); err != nil {
		t.Fatalf("connection ended after the string id: %v", err)
	}
	if answered["id"] != float64(7) || answered["result"] != "fast" {
		t.Fatalf("next request: got %v", answered)
	}
}
