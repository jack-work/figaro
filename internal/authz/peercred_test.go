package authz

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

func TestAdmitStampsThePeerUID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peercred is linux-only")
	}
	path := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, err := Admit(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	want := rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(os.Getuid())}
	if got := PrincipalOf(ctx); got != want {
		t.Fatalf("principal = %+v, want %+v", got, want)
	}
}

// Nothing vouches for a peer that did not come over a unix socket, so the
// door refuses it rather than serving it as nobody. A TCP or WebSocket
// listener must bring its own authenticator.
func TestAdmitRefusesAPeerPeercredCannotVouchFor(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if ctx, err := Admit(context.Background(), a); err == nil {
		t.Fatalf("admitted as %+v", PrincipalOf(ctx))
	}
	p, _ := NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	if _, err := p.Admit(context.Background(), a); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("peercred admitted a pipe: %v", err)
	}
}
