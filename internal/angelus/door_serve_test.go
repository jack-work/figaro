package angelus

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/authn"
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
			a.handleConn(ctx, authn.DoorLocal, server)
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
