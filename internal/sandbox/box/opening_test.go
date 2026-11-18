package box

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/sandbox"
)

func serveOne(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	sock := filepath.Join(sockDir(t), "s")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		nc, err := ln.Accept()
		if err == nil {
			handle(nc)
		}
	}()
	return sock
}

func TestOneConnectionIsServedAfterItsOpeningWasRead(t *testing.T) {
	ws := sockDir(t)
	srv, err := NewServer(ws)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan Opening, 1)
	done := make(chan struct{})
	sock := serveOne(t, func(nc net.Conn) {
		o, err := ReadOpening(nc)
		if err != nil {
			t.Error(err)
			nc.Close()
			return
		}
		seen <- o
		srv.ServeConn(context.Background(), nc, o.Read)
		close(done)
	})
	b, err := Provider{Name: "t", Socket: sock}.Open(context.Background(), sandbox.Spec{Figaro: "fig-1", Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	if o := <-seen; o.Scope != "fig-1" || o.Workspace != ws {
		t.Fatalf("opening %+v", o)
	}
	var out sink
	p, err := b.Exec(context.Background(), sandbox.ExecRequest{Command: "pwd", OnChunk: out.add})
	if err != nil {
		t.Fatal(err)
	}
	if res := p.Wait(); res.ExitCode != 0 || strings.TrimSpace(out.String()) != ws {
		t.Fatalf("exit %d output %q", res.ExitCode, out.String())
	}
	var bg sink
	if _, err := b.Sessions().Start(sandbox.ExecRequest{Command: "sleep 300 & echo $!; wait", OnChunk: bg.add}); err != nil {
		t.Fatal(err)
	}
	pid := pidOf(t, &bg)
	b.Close()
	<-done
	waitFor(t, "the session to die with its connection", func() bool { return !alive(pid) })
}

func TestARefusedOpeningReachesTheCaller(t *testing.T) {
	sock := serveOne(t, func(nc net.Conn) {
		defer nc.Close()
		o, err := ReadOpening(nc)
		if err != nil {
			t.Error(err)
			return
		}
		o.Refuse(nc, errors.New("valet: not for you"))
	})
	_, err := Provider{Name: "t", Socket: sock}.Open(context.Background(), sandbox.Spec{Figaro: "fig-1"})
	if err == nil || !strings.Contains(err.Error(), "valet: not for you") {
		t.Fatalf("open: %v", err)
	}
}

func TestAnOpeningMustComeFirst(t *testing.T) {
	for name, wire := range map[string]string{
		"method": `{"jsonrpc":"2.0","id":1,"method":"box.exec","params":{}}` + "\n",
		"json":   "nope\n",
		"eof":    `{"jsonrpc":"2.0"`,
	} {
		if _, err := ReadOpening(strings.NewReader(wire)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	long := strings.NewReader(`{"x":"` + strings.Repeat("a", maxOpening+1) + `"}` + "\n")
	if _, err := ReadOpening(long); err == nil || !strings.Contains(err.Error(), strconv.Itoa(maxOpening)) {
		t.Fatalf("long: %v", err)
	}
}
