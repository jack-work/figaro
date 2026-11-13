package angelus_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
)

// The pool's methods were figaro.slot.* before 0.69. The protocol version did
// not move, so an old broker still says hello; each old name must then die
// in the router, before the guard or any handler, and never touch a lease.
func TestAnOldSlotMethodIsNeverDispatched(t *testing.T) {
	r := leasingRig(t)
	dir, err := os.MkdirTemp("", "ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := angelus.ListenUnix(sock, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.a.Serve(ctx, ln, authz.EndpointLocal, nil); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c := jkrpc.NewClient(jkrpc.NewConn(nc), nil)
	t.Cleanup(func() { c.Close() })

	if err := c.Call(ctx, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}, nil); err != nil {
		t.Fatalf("hello: %v", err)
	}
	notFound := func(err error) bool {
		var jerr *jkrpc.Error
		return errors.As(err, &jerr) && jerr.Code == -32601
	}
	params := map[string]any{"pool": "groups", "key": "chat:old", "generation": 1, "id": "m1", "text": "hi", "seen": []int{}}

	if err := c.Call(ctx, rpc.MethodWorkerLease, map[string]any{"pool": "nowhere"}, nil); err == nil || notFound(err) {
		t.Fatalf("canary: the new name did not reach its handler: %v", err)
	}

	for _, old := range []string{"figaro.slot.pages", "figaro.slot.lease", "figaro.slot.release", "figaro.slot.deliver", "figaro.slot.say"} {
		if _, ok := r.h[old]; ok {
			t.Errorf("%s has a handler", old)
		}
		if err := c.Call(ctx, old, params, nil); !notFound(err) {
			t.Errorf("%s answered %v, want method not found", old, err)
		}
	}
	if r.a.Workers.InForce("worker0-g1") {
		t.Fatal("an old call leased a worker")
	}
	got, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:old"}`)
	if err != nil || got.Box != "worker0-g1" {
		t.Fatalf("after the old calls the first lease is %+v, %v; want worker0-g1", got, err)
	}
}
