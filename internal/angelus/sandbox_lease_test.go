package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/sandbox/agent"
	"github.com/jack-work/figaro/internal/sandbox/agent/agenttest"
	"github.com/jack-work/figaro/internal/sandbox/lease"
)

const leasePolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.helper]
permissions = ["create:*", "fork:*", "set:*", "read:*", "kill:*", "use:sandbox:local"]

[roles.lessee]
permissions = ["create:*", "use:sandbox:shell"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "helper"
principals = ["user:bob"]

[[bind]]
role = "lessee"
principals = ["user:carol"]
`

type probeLog struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (p *probeLog) probe(_ context.Context, name, exec string, writable []string, figaro string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, name)
	if p.fail[name] {
		return fmt.Errorf("probe: left in a writable dir: /work/canary")
	}
	return nil
}

type leaseRig struct {
	grantRig
	ag     *agenttest.Agent
	leases *angelus.SandboxLeases
	probes *probeLog
	p      *bashProvider
	dir    string
}

func newLeaseRig(t *testing.T, command, workload string) leaseRig {
	t.Helper()
	ag, err := agenttest.Start("spain", agenttest.Spec{Name: "shell", Workload: agent.Job, Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.Close)
	p := &bashProvider{command: command, turns: make(chan string, 4)}
	r := newRigProviding(t, leasePolicy, fmt.Sprintf("[sandbox.nodes.spain]\nagent = %q\nworkload = [%q]\n", ag.Endpoint, workload),
		func(string, provider.Knobs) (provider.Provider, error) { return p, nil })
	dir := t.TempDir()
	leases, err := angelus.OpenSandboxLeases(dir)
	if err != nil {
		t.Fatal(err)
	}
	probes := &probeLog{fail: map[string]bool{}}
	leases.Probe = probes.probe
	r.a.Leases = leases
	r.hs.RecoverSandboxLeases(context.Background())
	return leaseRig{grantRig: r, ag: ag, leases: leases, probes: probes, p: p, dir: dir}
}

func sandboxNamed(name string) *rpc.FormPatch {
	raw, _ := json.Marshal(name)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": raw}, nil)
	return &p
}

func (r leaseRig) createIn(ctx context.Context, sandbox string) (string, error) {
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Patch: sandboxNamed(sandbox)})
	out, err := r.h[rpc.MethodCreate](ctx, raw)
	if err != nil {
		return "", err
	}
	return out.(rpc.CreateResponse).FigaroID, nil
}

func (r leaseRig) fork(ctx context.Context, id string, patch *rpc.FormPatch) (string, error) {
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: id, Patch: patch})
	out, err := r.h[rpc.MethodFork](ctx, raw)
	if err != nil {
		return "", err
	}
	return out.(rpc.ForkResponse).Alternative, nil
}

func (r leaseRig) kill(t *testing.T, id string) {
	t.Helper()
	raw, _ := json.Marshal(rpc.KillRequest{FigaroID: id, Now: true})
	if _, err := r.h[rpc.MethodKill](asOwner(), raw); err != nil {
		t.Fatal(err)
	}
}

func (r leaseRig) set(t *testing.T, ctx context.Context, id, key, val string) error {
	v, _ := json.Marshal(val)
	var drops []string
	keys := map[string]json.RawMessage{key: v}
	if val == "" {
		keys, drops = nil, []string{key}
	}
	_, err := r.h[rpc.MethodSet](ctx, withAria(t, id, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, keys, drops)}))
	return err
}

func (r leaseRig) row(sandbox string) (lease.Row, bool) { return r.leases.Ledger.Of(sandbox) }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAFigaroNamingAClassLeasesASandboxAndItsShellRunsThere(t *testing.T) {
	r := newLeaseRig(t, `echo "$(pwd)" ; echo canary > canary`, agent.Job)
	id, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.sandboxOf(t, id); got != "shell0" {
		t.Fatalf("system.sandbox %q, want the leased sandbox shell0", got)
	}
	row, ok := r.row("shell0")
	if !ok || row.State != lease.Leased || !row.Holds(id) || row.Generation != 1 || row.Node != "spain" {
		t.Fatalf("ledger %+v %v", row, ok)
	}
	if st, gen := r.ag.State("shell0"); st != agent.Leased || gen != 1 {
		t.Fatalf("agent holds shell0 as %s/%d", st, gen)
	}
	if _, err := r.h[rpc.MethodQua](asOwner(), withAria(t, id, rpc.QuaRequest{Text: "go"})); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-r.p.turns:
		if !strings.Contains(out, r.ag.Workspace("shell0")) {
			t.Fatalf("bash ran in %q, want the leased sandbox's workspace %s", out, r.ag.Workspace("shell0"))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish")
	}
	if _, err := os.Stat(filepath.Join(r.ag.Workspace("shell0"), "canary")); err != nil {
		t.Fatalf("the shell's file is not in the sandbox: %v", err)
	}
}

func TestEverySandboxLeasedIsNoFreeSandbox(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	for range 2 {
		if _, err := r.createIn(asOwner(), "shell"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.createIn(asOwner(), "shell")
	if err == nil || !strings.Contains(err.Error(), "no free sandbox") {
		t.Fatalf("a third create on two sandboxes: %v", err)
	}
	if n := len(r.backend.ConversationIDs()); n != 2 {
		t.Fatalf("%d figaros in the store, want the refused birth discarded", n)
	}
}

func TestAForkSharesTheLeaseAndTheLastKillResetsTheSandbox(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	parent, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.ag.Workspace("shell0"), "canary"), []byte("x"), 0o600)
	child, err := r.fork(asOwner(), parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.sandboxOf(t, child); got != "shell0" {
		t.Fatalf("the fork runs in %q, want its parent's sandbox", got)
	}
	if row, _ := r.row("shell0"); !row.Holds(parent) || !row.Holds(child) {
		t.Fatalf("holders %v", row.Holders)
	}
	r.kill(t, child)
	if row, ok := r.row("shell0"); !ok || row.State != lease.Leased || !row.Holds(parent) || row.Holds(child) {
		t.Fatalf("after the fork's kill: %+v %v", row, ok)
	}
	r.kill(t, parent)
	waitFor(t, "the release", func() bool { _, ok := r.row("shell0"); return !ok })
	if st, _ := r.ag.State("shell0"); st != agent.Free {
		t.Fatalf("after the last kill the agent holds shell0 as %s", st)
	}
	if _, err := os.Stat(filepath.Join(r.ag.Workspace("shell0"), "canary")); err == nil {
		t.Fatal("the canary survived the release")
	}
	if !slicesContain(r.probes.calls, "shell0", 2) {
		t.Fatalf("probes %v: want one at lease and one after the reset", r.probes.calls)
	}
	again, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	if row, _ := r.row(r.sandboxOf(t, again)); row.Generation != 2 {
		t.Fatalf("the next lease is generation %d, want 2", row.Generation)
	}
}

func slicesContain(calls []string, name string, n int) bool {
	c := 0
	for _, s := range calls {
		if s == name {
			c++
		}
	}
	return c >= n
}

func TestNobodyNamesALeasedSandboxDirectly(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	held, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.createIn(asOwner(), "shell0"); err == nil {
		t.Fatal("the owner created into a leased sandbox by name")
	}
	if _, err := r.createIn(asOwner(), "shell1"); err == nil {
		t.Fatal("the owner created into a free sandbox by name")
	}
	plain, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.set(t, asOwner(), plain.FigaroID, "system.sandbox", "shell0"); err == nil {
		t.Fatal("the owner set a figaro into a leased sandbox by name")
	}
	if err := r.set(t, asOwner(), plain.FigaroID, "system.sandbox", "shell"); err == nil {
		t.Fatal("a figaro took a class after its birth")
	}
	if _, err := r.fork(asOwner(), plain.FigaroID, sandboxNamed("shell0")); err == nil {
		t.Fatal("a fork of an unbound figaro joined a leased sandbox by name")
	}
	raw, _ := json.Marshal(rpc.FormCreateRequest{Patch: sandboxNamed("shell0")})
	if _, err := r.h[rpc.MethodFormCreate](asOwner(), raw); err == nil {
		t.Fatal("an unbound form named a leased sandbox")
	}
	if row, _ := r.row("shell0"); len(row.Holders) != 1 || !row.Holds(held) {
		t.Fatalf("holders %v, want the lessee alone", row.Holders)
	}
}

func TestOnlyTheOwnerMovesAFigaroOutOfItsLeasedSandbox(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	id, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"", "local"} {
		if err := r.set(t, as("user", "bob"), id, "system.sandbox", to); err == nil || !strings.Contains(err.Error(), "only the owner") {
			t.Fatalf("bob moved the figaro to %q: %v", to, err)
		}
	}
	if row, _ := r.row("shell0"); !row.Holds(id) {
		t.Fatal("a refused move dropped the holder")
	}
	if err := r.set(t, asOwner(), id, "system.sandbox", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the release after the owner's move", func() bool { _, ok := r.row("shell0"); return !ok })
	if st, _ := r.ag.State("shell0"); st != agent.Free {
		t.Fatalf("agent holds shell0 as %s", st)
	}
}

func TestOnlyAPrincipalWithUseOnTheClassLeases(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	if err := os.WriteFile(filepath.Join(r.a.Settings.ConfigDir, "outfits", "leased.toml"), []byte("[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \"shell\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"bob", "mallory"} {
		if _, err := r.create(as("user", who), "leased"); err == nil {
			t.Fatalf("%s leased with no use on sandbox:shell", who)
		}
	}
	if _, err := r.createIn(as("user", "carol"), "shell"); err == nil {
		t.Fatal("carol named system.sandbox in her own patch; it is the owner's key")
	}
	for _, c := range r.ag.Calls() {
		if c == agent.MethodLease {
			t.Fatal("a refused principal reached the agent's lease")
		}
	}
	id, err := r.create(as("user", "carol"), "leased")
	if err != nil {
		t.Fatalf("carol holds use on sandbox:shell and an outfit names the class: %v", err)
	}
	if got := r.sandboxOf(t, id.FigaroID); got != "shell0" {
		t.Fatalf("carol's figaro runs in %q", got)
	}
}

func TestAClassOfAWorkloadTheNodeDoesNotHostIsNotOffered(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Conversation)
	if r.leases.IsClass("shell") {
		t.Fatal("a job class is offered on a node that hosts conversations only")
	}
	if _, err := r.createIn(asOwner(), "shell"); err == nil {
		t.Fatal("a figaro was born naming a class nobody offers")
	}
	for _, c := range r.ag.Calls() {
		if c == agent.MethodLease {
			t.Fatal("the agent was asked for a lease of a class not offered")
		}
	}
}

func TestASandboxThatFailsItsProbeIsCordonedUntilReinstated(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	id, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	r.probes.mu.Lock()
	r.probes.fail["shell0"] = true
	r.probes.mu.Unlock()
	r.kill(t, id)
	waitFor(t, "the cordon", func() bool { row, _ := r.row("shell0"); return row.State == lease.Cordoned })
	if st, _ := r.ag.State("shell0"); st != agent.Cordoned {
		t.Fatalf("agent holds shell0 as %s", st)
	}
	raw, _ := json.Marshal(rpc.SandboxReinstateRequest{Sandbox: "shell0"})
	if _, err := r.h[rpc.MethodSandboxReinstate](asOwner(), raw); err == nil {
		t.Fatal("a sandbox that still fails its probe was reinstated")
	}
	if _, err := r.h[rpc.MethodSandboxReinstate](as("user", "bob"), raw); err == nil {
		t.Fatal("bob reinstated a sandbox")
	}
	r.probes.mu.Lock()
	r.probes.fail["shell0"] = false
	r.probes.mu.Unlock()
	if _, err := r.h[rpc.MethodSandboxReinstate](asOwner(), raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.row("shell0"); ok {
		t.Fatal("a reinstated sandbox still has a ledger row")
	}
	if st, _ := r.ag.State("shell0"); st != agent.Free {
		t.Fatalf("agent holds shell0 as %s", st)
	}
}

func TestAReceiptWithoutTheReleasesNonceCordonsTheSandbox(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	id, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	r.ag.ForgeNonce = true
	r.kill(t, id)
	waitFor(t, "the cordon", func() bool { row, _ := r.row("shell0"); return row.State == lease.Cordoned })
}

func TestARestartRecoversLeasesAndReleasesWhatTheLedgerDoesNotHold(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	id, err := r.createIn(asOwner(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	c, err := agent.Dial(context.Background(), r.ag.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Lease(context.Background(), agent.LeaseRequest{Class: "shell", Generation: 9, Figaro: strings.Repeat("e", 32)}); err != nil {
		t.Fatal(err)
	}
	r.a.Settings.UnbindSandbox("shell0")
	fresh, err := angelus.OpenSandboxLeases(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Probe = r.probes.probe
	r.a.Leases = fresh
	r.hs.RecoverSandboxLeases(context.Background())
	if !r.a.Settings.BoundSandbox("shell0") {
		t.Fatal("a live lease was not bound again after the restart")
	}
	if row, _ := fresh.Ledger.Of("shell0"); !row.Holds(id) {
		t.Fatalf("holders after restart %v", row.Holders)
	}
	row, ok := fresh.Ledger.Of("shell1")
	if !ok || row.State != lease.Releasing || row.Generation != 9 {
		t.Fatalf("the agent's lease the ledger lacked: %+v %v", row, ok)
	}
	r.hs.FinishSandboxLeases(context.Background())
	if _, ok := fresh.Ledger.Of("shell1"); ok {
		t.Fatal("the stray lease was not released")
	}
	if st, _ := r.ag.State("shell1"); st != agent.Free {
		t.Fatalf("agent holds shell1 as %s", st)
	}
}

func TestAFigaroThatIsNotAHolderRunsNothingInTheSandbox(t *testing.T) {
	r := newLeaseRig(t, "true", agent.Job)
	if _, err := r.createIn(asOwner(), "shell"); err != nil {
		t.Fatal(err)
	}
	if err := r.hs.MayRunIn("shell0", strings.Repeat("d", 32)); err == nil {
		t.Fatal("a figaro outside the lease may run in it")
	}
	if err := r.hs.MayRunIn("shell1", strings.Repeat("d", 32)); err == nil {
		t.Fatal("a figaro may run in a free sandbox")
	}
}
