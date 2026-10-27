package box

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
)

// The box owns the directory its exec socket lives in, so it decides what that
// path is. A symlink there could point the daemon at any socket it can reach:
// another box, its own door, hush.
func TestTheProviderNeverFollowsALinkToAnotherSocket(t *testing.T) {
	dir := sockDir(t)
	canaryPath := filepath.Join(dir, "canary.sock")
	canary, err := net.Listen("unix", canaryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer canary.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := canary.Accept(); err == nil {
			accepted <- struct{}{}
			c.Close()
		}
	}()

	exec := filepath.Join(dir, "exec.sock")
	if err := os.Symlink(canaryPath, exec); err != nil {
		t.Fatal(err)
	}
	_, err = Provider{Name: "t", Socket: exec, Scope: "a"}.Open(context.Background(), sandbox.Spec{})
	if err == nil {
		t.Fatal("the provider dialed through a symlink")
	}
	select {
	case <-accepted:
		t.Fatal("the canary socket saw a connection")
	case <-time.After(200 * time.Millisecond):
	}

	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Provider{Name: "t", Socket: plain, Scope: "a"}).Open(context.Background(), sandbox.Spec{}); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("a regular file at the exec path: %v", err)
	}

	s := serve(t)
	if _, err := (Provider{Name: "t", Socket: s.sock, Scope: "a"}).Open(context.Background(), sandbox.Spec{}); err != nil {
		t.Fatalf("canary: the real socket does not dial, so the refusal above proves nothing: %v", err)
	}
}
