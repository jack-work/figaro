package angelus_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(ents)
}

// Issue #58: a connection whose peer hung up left the daemon's end open
// until a GC finalizer ran, so an endpoint with many short clients held one fd
// per client it had ever served. GC is off here so no finalizer can close
// what the endpoint forgot to.
func TestAEndpointClosesAConnectionWhenItsPeerHangsUp(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	r := newGrantRig(t)
	sock := filepath.Join(t.TempDir(), "door.sock")
	ln, err := angelus.ListenUnix(sock, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.a.Serve(ctx, ln, authz.EndpointLocal, nil); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	call := func() {
		nc, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		c := jkrpc.NewClient(jkrpc.NewConn(nc), nil)
		var out rpc.HelloResponse
		if err := c.Call(context.Background(), rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}, &out); err != nil {
			t.Fatalf("hello: %v", err)
		}
		c.Close()
	}
	call()
	base := openFDs(t)

	const n = 64
	for range n {
		call()
	}
	deadline := time.Now().Add(5 * time.Second)
	var now int
	for {
		if now = openFDs(t); now <= base+4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if now > base+4 {
		t.Fatalf("after %d connections ended the process holds %d fds, %d before: the door kept its end of each", n, now, base)
	}
}
