package local

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
)

type fakeProc struct{ done chan struct{} }

func (f *fakeProc) Wait() sandbox.Result        { <-f.done; return sandbox.Result{} }
func (f *fakeProc) Done() <-chan struct{}       { return f.done }
func (f *fakeProc) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeProc) Interrupt() error            { return nil }
func (f *fakeProc) PTY() bool                   { return false }
func (f *fakeProc) Kill() error {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
	return nil
}

func fakeStart(sandbox.ExecRequest) (sandbox.Process, error) {
	return &fakeProc{done: make(chan struct{})}, nil
}

func TestHostScopesSessions(t *testing.T) {
	host := NewHost(DefaultTTL)
	a, b := host.Scoped("a", fakeStart), host.Scoped("b", fakeStart)
	s, err := a.Start(sandbox.ExecRequest{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Start(sandbox.ExecRequest{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(s.ID()); err != nil {
		t.Fatal("session invisible from its own scope")
	}
	if _, err := b.Get(s.ID()); err != sandbox.ErrNoSession {
		t.Fatal("session visible from another scope")
	}
	if got := len(a.List()); got != 1 {
		t.Fatalf("List = %d sessions, want 1", got)
	}
	if n := host.KillScope("a"); n != 1 || host.Count() != 1 {
		t.Fatalf("KillScope took %d, %d left; want 1 and 1", n, host.Count())
	}
}

func TestHostReapsFinishedNotRunning(t *testing.T) {
	host := NewHost(40 * time.Millisecond)
	v := host.Scoped("x", fakeStart)
	done, _ := v.Start(sandbox.ExecRequest{Command: "true"})
	running, _ := v.Start(sandbox.ExecRequest{Command: "sleep"})
	done.Kill()
	<-done.Done()
	if _, err := v.Get(done.ID()); err != nil {
		t.Fatal("finished session should linger until TTL elapses")
	}
	time.Sleep(70 * time.Millisecond)
	if _, err := v.Get(done.ID()); err == nil {
		t.Fatal("finished session should be reaped after TTL")
	}
	if _, err := v.Get(running.ID()); err != nil {
		t.Fatal("running session must never be reaped")
	}
}

func TestCapBufferDropsFront(t *testing.T) {
	b := capBuffer{cap: 4}
	b.write([]byte("abcdef"))
	if got := string(b.buf); got != "cdef" {
		t.Fatalf("capBuffer = %q, want %q", got, "cdef")
	}
}
