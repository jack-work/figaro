package authz

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
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

func TestAdmitLeavesANonUnixPeerAnonymous(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, err := Admit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if got := PrincipalOf(ctx); got != (rpc.Principal{}) {
		t.Fatalf("principal = %+v, want anonymous", got)
	}
}
