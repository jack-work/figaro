package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/agent"
	"github.com/jack-work/figaro/internal/sandbox/agent/agenttest"
	"github.com/jack-work/figaro/internal/sandbox/box"
)

const fig = "0123456789abcdef0123456789abcdef"

func nonce(n int) string { return fmt.Sprintf("%064x", n) }

func start(t *testing.T) (*agenttest.Agent, *agent.Client) {
	t.Helper()
	a, err := agenttest.Start("spain", agenttest.Spec{Name: "shell", Workload: agent.Job, Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	c, err := agent.Dial(context.Background(), a.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return a, c
}

func TestAnEndpointIsAUnixPathAndNothingElse(t *testing.T) {
	for _, bad := range []string{"", "/run/a.sock", "tcp://host:1", "unix://host/run/a.sock", "unix://relative", "unix:///run/../a.sock", "unix:///run/a.sock?x=1"} {
		if _, err := agent.SocketPath(bad); err == nil {
			t.Errorf("%q: want refused", bad)
		}
	}
	if p, err := agent.SocketPath("unix:///run/sandbox-agent/agent.sock"); err != nil || p != "/run/sandbox-agent/agent.sock" {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestALeaseNamesASandboxAndItsExecEndpoint(t *testing.T) {
	_, c := start(t)
	ctx := context.Background()
	if c.Node() != "spain" {
		t.Fatalf("node %q", c.Node())
	}
	classes, err := c.Classes(ctx)
	if err != nil || len(classes) != 1 || classes[0].Workload != agent.Job || len(classes[0].Sandboxes) != 2 {
		t.Fatalf("classes %+v, %v", classes, err)
	}
	l, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 1, Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 1, Figaro: fig})
	if err != nil || again != l {
		t.Fatalf("a repeated lease call got %+v, %v; want the same answer %+v", again, err, l)
	}
	path, err := agent.SocketPath(l.Exec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := box.Provider{Name: l.Sandbox, Socket: path}.Open(ctx, sandbox.Spec{Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var out strings.Builder
	p, err := b.Exec(ctx, sandbox.ExecRequest{Command: "echo hi", OnChunk: func(c []byte) { out.Write(c) }})
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Wait(); r.ExitCode != 0 || !strings.Contains(out.String(), "hi") {
		t.Fatalf("exec in the leased sandbox: %+v, %q", r, out.String())
	}
}

func TestTheAgentRefusesWithANamedReason(t *testing.T) {
	_, c := start(t)
	ctx := context.Background()
	for g := uint64(1); g <= 2; g++ {
		if _, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: g, Figaro: fig}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 3, Figaro: fig})
	if !agent.RefusedFor(err, agent.NoFreeSandbox) {
		t.Fatalf("third lease on two sandboxes: %v", err)
	}
	if _, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 1, Figaro: strings.Repeat("f", 32)}); !agent.RefusedFor(err, agent.GenerationUsed) && !agent.RefusedFor(err, agent.NoFreeSandbox) {
		t.Fatalf("a used generation: %v", err)
	}
	if _, err := c.Lease(ctx, agent.LeaseRequest{Class: "nope", Generation: 9, Figaro: fig}); !agent.RefusedFor(err, agent.UnknownClass) {
		t.Fatalf("unknown class: %v", err)
	}
	if _, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 9, Figaro: "not-an-id"}); err == nil {
		t.Fatal("a malformed figaro id left the client")
	}
}

func TestAResetAnswersTheReceiptForItsNonceAndEmptiesTheSandbox(t *testing.T) {
	a, c := start(t)
	ctx := context.Background()
	l, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 1, Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := agent.SocketPath(l.Exec)
	b, err := box.Provider{Name: l.Sandbox, Socket: path}.Open(ctx, sandbox.Spec{Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := b.Exec(ctx, sandbox.ExecRequest{Command: "echo canary > canary"})
	p.Wait()
	b.Close()

	r, err := c.Reset(ctx, agent.ResetRequest{Sandbox: l.Sandbox, Generation: 1, Nonce: nonce(1)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Nonce != nonce(1) || r.Generation != 1 {
		t.Fatalf("receipt %+v", r)
	}
	if st, _ := a.State(l.Sandbox); st != agent.Free {
		t.Fatalf("after reset: %s", st)
	}
	again, err := c.Reset(ctx, agent.ResetRequest{Sandbox: l.Sandbox, Generation: 1, Nonce: nonce(1)})
	if err != nil || !again.At.Equal(r.At) {
		t.Fatalf("a repeat with the same nonce got %+v, %v; want the same receipt", again, err)
	}
	if _, err := c.Reset(ctx, agent.ResetRequest{Sandbox: l.Sandbox, Generation: 1, Nonce: nonce(2)}); err != nil {
		t.Fatalf("a new nonce from free at the last generation: %v", err)
	}
	b, err = box.Provider{Name: l.Sandbox, Socket: path}.Open(ctx, sandbox.Spec{Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.FS().ReadFile("canary"); err == nil {
		t.Fatal("the canary survived the reset")
	}
}

func TestAReceiptThatDoesNotCarryTheNonceIsRefused(t *testing.T) {
	a, c := start(t)
	ctx := context.Background()
	l, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 1, Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	a.ForgeNonce = true
	if _, err := c.Reset(ctx, agent.ResetRequest{Sandbox: l.Sandbox, Generation: 1, Nonce: nonce(7)}); err == nil {
		t.Fatal("a receipt with another nonce was taken")
	}
}

func TestAFailedResetCordonsTheSandbox(t *testing.T) {
	a, c := start(t)
	ctx := context.Background()
	l, err := c.Lease(ctx, agent.LeaseRequest{Class: "shell", Generation: 1, Figaro: fig})
	if err != nil {
		t.Fatal(err)
	}
	a.FailReset[l.Sandbox] = "start"
	_, err = c.Reset(ctx, agent.ResetRequest{Sandbox: l.Sandbox, Generation: 1, Nonce: nonce(3)})
	if !agent.RefusedFor(err, agent.ResetFailed) {
		t.Fatalf("got %v", err)
	}
	if st, _ := a.State(l.Sandbox); st != agent.Cordoned {
		t.Fatalf("after a failed reset: %s", st)
	}
}
