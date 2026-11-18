//go:build linux

package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/figaro/internal/toolcgroup"
)

// Real cgroups only: run inside a systemd user scope with Delegate=yes
// (scripts/peer-check-e2e.sh). Elsewhere the test skips, never fakes.
func delegated(t *testing.T) {
	t.Helper()
	if _, err := toolcgroup.Own(toolcgroup.NewRegistry()); err != nil || os.Getenv("FIGARO_PEER_CHECK_E2E") == "" {
		t.Skipf("needs a delegated cgroup and FIGARO_PEER_CHECK_E2E=1: %v", err)
	}
}

func admitFrom(t *testing.T, host *local.Host, peer *authz.Peercred, figaro, shell string) (context.Context, error) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	sb, err := local.Provider{Host: host}.Open(t.Context(), sandbox.Spec{Figaro: figaro, Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	connect := filepath.Join(t.TempDir(), "connect")
	if err := os.WriteFile(connect, []byte("#!/usr/bin/env python3\nimport socket, sys, time\ns = socket.socket(socket.AF_UNIX)\ns.connect(sys.argv[1])\ntime.sleep(5)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := "C='" + connect + " " + sock + "'; " + shell
	p, err := sb.Exec(t.Context(), sandbox.ExecRequest{Command: cmd, Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	defer p.Kill()
	ln.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("no connection from the tool: %v", err)
	}
	defer conn.Close()
	return peer.Admit(t.Context(), conn)
}

func TestACallerInAFigarosToolCgroupIsThatFigaro(t *testing.T) {
	delegated(t)
	peer, err := authz.NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	if err != nil {
		t.Fatal(err)
	}
	host := local.NewHost(time.Minute)
	attributeTools(host, peer)
	figaro := "f" + strconv.Itoa(os.Getpid())
	for name, shell := range map[string]string{
		"plain":       `env -u FIGARO_ARIA $C`,
		"setsid":      `setsid -w $C`,
		"double fork": `( ( setsid $C & ) & ); sleep 6`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, err := admitFrom(t, host, peer, figaro, shell)
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := authz.ToolOf(ctx); !ok || got != figaro {
				t.Fatalf("attributed to %q, want figaro %s", got, figaro)
			}
			sub, _ := authz.SubjectOf(ctx)
			if authz.OwnerCall(ctx, peer.Self(), sub) {
				t.Fatal("a tool caller passed as the owner")
			}
		})
	}
}

func TestACallerOutsideEveryToolCgroupIsTheOwner(t *testing.T) {
	delegated(t)
	peer, err := authz.NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	if err != nil {
		t.Fatal(err)
	}
	attributeTools(local.NewHost(time.Minute), peer)
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("unix", sock)
		if err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, err := peer.Admit(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := authz.SubjectOf(ctx)
	if _, tool := authz.ToolOf(ctx); tool || !authz.OwnerCall(ctx, peer.Self(), sub) {
		t.Fatal("the owner outside every tool cgroup was not the owner")
	}
}

func benchStart(b *testing.B, host *local.Host) {
	sb, err := local.Provider{Host: host}.Open(b.Context(), sandbox.Spec{Figaro: "bench" + strconv.Itoa(os.Getpid()), Workspace: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		p, err := sb.Exec(b.Context(), sandbox.ExecRequest{Command: "true"})
		if err != nil {
			b.Fatal(err)
		}
		p.Wait()
	}
}

func BenchmarkToolStartUnattributed(b *testing.B) {
	benchStart(b, local.NewHost(time.Minute))
}

func BenchmarkToolStartInItsCgroup(b *testing.B) {
	if os.Getenv("FIGARO_PEER_CHECK_E2E") == "" {
		b.Skip("needs FIGARO_PEER_CHECK_E2E=1 inside a delegated scope")
	}
	peer, _ := authz.NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	host := local.NewHost(time.Minute)
	attributeTools(host, peer)
	benchStart(b, host)
}

func BenchmarkToolCgroupFirstCall(b *testing.B) {
	if os.Getenv("FIGARO_PEER_CHECK_E2E") == "" {
		b.Skip("needs FIGARO_PEER_CHECK_E2E=1 inside a delegated scope")
	}
	tree, err := toolcgroup.Own(toolcgroup.NewRegistry())
	if err != nil {
		b.Fatal(err)
	}
	n := 0
	for b.Loop() {
		n++
		if _, err := tree.FD("first" + strconv.Itoa(os.Getpid()) + "-" + strconv.Itoa(n)); err != nil {
			b.Fatal(err)
		}
	}
	for i := 1; i <= n; i++ {
		os.Remove(filepath.Join(toolcgroup.Mount, tree.CgroupOf("first"+strconv.Itoa(os.Getpid())+"-"+strconv.Itoa(i))))
	}
}

func BenchmarkAttributeACaller(b *testing.B) {
	reg := toolcgroup.NewRegistry()
	for i := range 2500 {
		reg.Register("/user.slice/app.slice/figaro-angelus.scope/tools-"+strconv.Itoa(i), strconv.Itoa(i))
	}
	pid := os.Getpid()
	for b.Loop() {
		cg, err := toolcgroup.ProcCgroupOf(pid)
		if err != nil {
			b.Fatal(err)
		}
		reg.Lookup(cg)
	}
}
