package box

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
)

type served struct {
	sock, ws string
	stop     func()
}

func serve(t *testing.T) served {
	t.Helper()
	dir := sockDir(t)
	ws := filepath.Join(dir, "ws")
	os.Mkdir(ws, 0o755)
	sock := filepath.Join(dir, "s")
	srv, err := NewServer(ws)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return served{sock: sock, ws: ws, stop: stop}
}

func open(t *testing.T, s served, scope string) *Box {
	t.Helper()
	b, err := Provider{Name: "t", Socket: s.sock, Scope: scope}.Open(context.Background(), sandbox.Spec{Env: map[string]string{"BOXVAR": "here"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b.(*Box)
}

type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *sink) add(b []byte) { s.mu.Lock(); s.buf.Write(b); s.mu.Unlock() }
func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestExecStreamsInOrderAndReportsExit(t *testing.T) {
	b := open(t, serve(t), "a")
	var out sink
	p, err := b.Exec(context.Background(), sandbox.ExecRequest{
		Command: `for i in 1 2 3; do echo $i; done; echo $BOXVAR; pwd; exit 7`,
		OnChunk: out.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Wait()
	if res.ExitCode != 7 {
		t.Fatalf("exit %d, want 7", res.ExitCode)
	}
	want := "1\n2\n3\nhere\n" + b.Workspace() + "\n"
	if got := out.String(); got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if iso := b.Isolation(); iso.Provider != "box" || !iso.Persistent || iso.FS || iso.Secrets {
		t.Fatalf("isolation %+v: want provider box claiming only persistence", iso)
	}
}

func TestStdinReachesTheProcess(t *testing.T) {
	b := open(t, serve(t), "a")
	var out sink
	p, err := b.Exec(context.Background(), sandbox.ExecRequest{Command: "read x; echo got:$x", OnChunk: out.add})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if res := p.Wait(); res.ExitCode != 0 || out.String() != "got:hi\n" {
		t.Fatalf("exit %d output %q", res.ExitCode, out.String())
	}
}

func pidOf(t *testing.T, out *sink) int {
	t.Helper()
	var pid int
	waitFor(t, "a grandchild pid", func() bool {
		line, _, ok := strings.Cut(out.String(), "\n")
		if !ok {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(line))
		return pid > 0
	})
	return pid
}

func TestCancelKillsTheProcessGroupInTheBox(t *testing.T) {
	b := open(t, serve(t), "a")
	ctx, cancel := context.WithCancel(context.Background())
	var out sink
	p, err := b.Exec(ctx, sandbox.ExecRequest{Command: "sleep 300 & echo $!; wait", OnChunk: out.add})
	if err != nil {
		t.Fatal(err)
	}
	child := pidOf(t, &out)
	cancel()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the process")
	}
	if !p.Wait().Canceled {
		t.Fatalf("result %+v: want Canceled", p.Wait())
	}
	waitFor(t, "the backgrounded grandchild to die", func() bool { return !alive(child) })
}

func TestSessionKillReachesTheGroupAndSurvivesAReconnect(t *testing.T) {
	s := serve(t)
	b := open(t, s, "a")
	var out sink
	sess, err := b.Sessions().Start(sandbox.ExecRequest{Command: "sleep 300 & echo $!; wait", OnChunk: out.add})
	if err != nil {
		t.Fatal(err)
	}
	child := pidOf(t, &out)

	other := open(t, s, "a")
	got, err := other.Sessions().Get(sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := open(t, s, "b").Sessions().Get(sess.ID()); !errors.Is(err, sandbox.ErrNoSession) {
		t.Fatalf("another scope saw the session: %v", err)
	}
	if err := got.Kill(); err != nil {
		t.Fatal(err)
	}
	for _, x := range []sandbox.Session{sess, got} {
		select {
		case <-x.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("kill did not end the session")
		}
	}
	waitFor(t, "the grandchild to die", func() bool { return !alive(child) })
	if st := got.Info().State; st != sandbox.SessionKilled {
		t.Fatalf("state %s, want killed", st)
	}
	if !strings.Contains(string(got.Log()), strconv.Itoa(child)) {
		t.Fatalf("log %q lost the transcript", got.Log())
	}
	if err := other.Sessions().Remove(sess.ID()); err != nil {
		t.Fatal(err)
	}
	if n := len(b.Sessions().List()); n != 0 {
		t.Fatalf("%d sessions after remove", n)
	}
}

func TestADroppedSocketFailsRunningCalls(t *testing.T) {
	s := serve(t)
	b := open(t, s, "a")
	var out sink
	p, err := b.Exec(context.Background(), sandbox.ExecRequest{Command: "sleep 300 & echo $!; wait", OnChunk: out.add})
	if err != nil {
		t.Fatal(err)
	}
	child := pidOf(t, &out)
	sess, err := b.Sessions().Start(sandbox.ExecRequest{Command: "sleep 300"})
	if err != nil {
		t.Fatal(err)
	}
	s.stop()
	for _, x := range []sandbox.Process{p, sess} {
		select {
		case <-x.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("a running call hung on a dropped socket")
		}
		if x.Wait().ExitCode != -1 {
			t.Fatalf("result %+v: want exit -1", x.Wait())
		}
	}
	if !strings.Contains(out.String(), "lost the connection") {
		t.Fatalf("output %q does not say the box went away", out.String())
	}
	if _, err := b.FS().ReadFile("x"); err == nil {
		t.Fatal("a call on a dead box succeeded")
	}
	waitFor(t, "the exec's group to die with the server", func() bool { return !alive(child) })
}

func TestRedialAfterTheServerReturns(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s")
	start := func() func() {
		srv, err := NewServer(dir)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := Listen(sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { srv.Serve(ctx, ln); close(done) }()
		return func() { cancel(); <-done }
	}
	stop := start()
	b, err := Provider{Name: "t", Socket: sock, Scope: "a"}.Open(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	stop()
	waitFor(t, "the link to drop", func() bool { _, err := b.FS().Stat("."); return err != nil })
	defer start()()
	if _, err := b.FS().Stat("."); err != nil {
		t.Fatalf("no redial: %v", err)
	}
}

func TestFSOverTheWire(t *testing.T) {
	s := serve(t)
	b := open(t, s, "a")
	f := b.FS()
	p := filepath.Join(b.Workspace(), "d", "f")
	if err := f.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := f.Stat(p)
	if err != nil || fi.Size() != 3 || fi.IsDir() || fi.Mode().Perm() != 0o600 || fi.Name() != "f" {
		t.Fatalf("stat %v %v", fi, err)
	}
	if err := f.Update(p, func(old []byte) ([]byte, error) { return append(old, "+two"...), nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename(p, p+"2"); err != nil {
		t.Fatal(err)
	}
	data, err := f.ReadFile(p + "2")
	if err != nil || string(data) != "one+two" {
		t.Fatalf("read %q %v", data, err)
	}
	_, err = f.Stat(p)
	if !errors.Is(err, fs.ErrNotExist) || !os.IsNotExist(err) {
		t.Fatalf("stat of a moved file: %v, want not-exist both ways", err)
	}
}

func TestUpdateIsSerializedAcrossClients(t *testing.T) {
	s := serve(t)
	p := filepath.Join(s.ws, "n")
	os.WriteFile(p, nil, 0o644)
	var wg sync.WaitGroup
	for range 4 {
		b := open(t, s, "a")
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if err := b.FS().Update(p, func(old []byte) ([]byte, error) { return append(old, 'x'), nil }); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if data, _ := os.ReadFile(p); len(data) != 20 {
		t.Fatalf("%d updates landed, want 20", len(data))
	}
}

func TestListenReplacesAStaleSocketButNotALiveOne(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(sock); err == nil {
		t.Fatal("took over a live socket")
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	ln2, err := Listen(sock)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	ln2.Close()
}

func TestWorkspaceResolvesUnderTheServedRoot(t *testing.T) {
	s := serve(t)
	os.Mkdir(filepath.Join(s.ws, "sub"), 0o755)
	for _, c := range []struct{ asked, want string }{
		{"", s.ws},
		{"sub", filepath.Join(s.ws, "sub")},
		{"missing", s.ws},
	} {
		b, err := Provider{Name: "t", Socket: s.sock, Scope: "a"}.Open(context.Background(), sandbox.Spec{Workspace: c.asked})
		if err != nil {
			t.Fatal(err)
		}
		if got := b.(*Box).Workspace(); got != c.want {
			t.Errorf("workspace %q -> %q, want %q", c.asked, got, c.want)
		}
		b.Close()
	}
}

func TestRelativePathsResolveAgainstTheOpenedWorkspace(t *testing.T) {
	s := serve(t)
	os.MkdirAll(filepath.Join(s.ws, "sub", "d"), 0o755)
	b, err := Provider{Name: "t", Socket: s.sock, Scope: "a"}.Open(context.Background(), sandbox.Spec{Workspace: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.FS().WriteFile("d/f", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.ws, "sub", "d", "f")); err != nil {
		t.Fatalf("relative write landed elsewhere: %v", err)
	}
	if fi, err := b.FS().Stat("d"); err != nil || !fi.IsDir() {
		t.Fatalf("stat d: %v %v", fi, err)
	}
}

func TestKillScopeReachesOnlyItsScopeInTheBox(t *testing.T) {
	s := serve(t)
	a, b := open(t, s, "a"), open(t, s, "b")
	sa, err := a.Sessions().Start(sandbox.ExecRequest{Command: "sleep 300"})
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.Sessions().Start(sandbox.ExecRequest{Command: "sleep 300"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := Provider{Name: "t", Socket: s.sock, Scope: "a"}.KillScope(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("KillScope = %d, %v; want 1", n, err)
	}
	select {
	case <-sa.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("scope a's session survived")
	}
	if st := sb.Info().State; st != sandbox.SessionRunning {
		t.Fatalf("scope b's session is %s", st)
	}
	sb.Kill()
}

func TestARemovedSessionKeepsTheLogItStreamed(t *testing.T) {
	b := open(t, serve(t), "a")
	sess, err := b.Sessions().Start(sandbox.ExecRequest{Command: "echo kept; exit 2"})
	if err != nil {
		t.Fatal(err)
	}
	if res := sess.Wait(); res.ExitCode != 2 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if err := b.Sessions().Remove(sess.ID()); err != nil {
		t.Fatal(err)
	}
	if got := string(sess.Log()); got != "kept\n" {
		t.Fatalf("log after remove %q", got)
	}
	if info := sess.Info(); info.State != sandbox.SessionExited || info.ExitCode != 2 {
		t.Fatalf("info after remove %+v", info)
	}
}
