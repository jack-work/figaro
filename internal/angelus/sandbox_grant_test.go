package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

const grantPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.box]
grants = [{ verbs = ["create", "fork"], on = ["*"] }, { verbs = ["use"], on = ["sandbox:dev-3"] }]

[roles.remote]
grants = [{ verbs = ["create"], on = ["*"] }, { verbs = ["use"], on = ["sandbox:bwrap"] }]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "box"
principals = ["sandbox:dev-3"]

[[bind]]
role = "remote"
principals = ["user:remote"]
`

type grantRig struct {
	h       map[string]func(context.Context, json.RawMessage) (any, error)
	backend *store.XwalBackend
	cwd     string
	policy  string
	a       *angelus.Angelus
}

const defaultPolicy = "<the default policy>"

func newGrantRig(t *testing.T) grantRig {
	t.Helper()
	return newRigWith(t, grantPolicy)
}

func newRigWith(t *testing.T, policyText string) grantRig {
	t.Helper()
	return newRigConfigured(t, policyText, "")
}

func newRigConfigured(t *testing.T, policyText, extraConfig string) grantRig {
	t.Helper()
	return newRigProviding(t, policyText, extraConfig, func(string, provider.Knobs) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	})
}

func newRigProviding(t *testing.T, policyText, extraConfig string, factory func(string, provider.Knobs) (provider.Provider, error)) grantRig {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "outfits"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"mock":   "[system]\nprovider = \"mock\"\nmodel = \"m\"\n",
		"boxed":  "[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \"bwrap\"\n",
		"judged": "[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \"judge\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "outfits", name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	self := strconv.Itoa(os.Getuid())
	policy, authzConfig := "", ""
	if policyText != defaultPolicy {
		policy, authzConfig = filepath.Join(dir, "policy.toml"), "[authz]\npolicy = \"policy.toml\"\n"
		if err := os.WriteFile(policy, []byte(strings.ReplaceAll(policyText, "SELF", self)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(authzConfig+extraConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})
	peer, err := authz.NewPeercred(uint32(os.Getuid()), config.PeercredConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a.Peercred = peer
	a.Policies = authz.NewPolicies(peer.Self(), angelus.StoreFacts(backend))
	a.Policies.SetBoxes(loaded.DooredBoxes())
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hs := angelus.NewHandlers(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		Ctx:             ctx,
		ProviderFactory: factory,
	})
	t.Cleanup(func() {
		for _, id := range backend.ConversationIDs() {
			_ = hs.RemoveAria(id, true)
		}
	})
	h := map[string]func(context.Context, json.RawMessage) (any, error){}
	for k, v := range hs.Map {
		h[k] = v
	}
	a.Handlers = hs.Map
	return grantRig{h: h, backend: backend, cwd: t.TempDir(), policy: policy, a: a}
}

func as(kind, id string) context.Context {
	return authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalKind(kind), ID: id})
}

func (r grantRig) create(ctx context.Context, outfit string) (rpc.CreateResponse, error) {
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{outfit}, Cwd: r.cwd})
	out, err := r.h[rpc.MethodCreate](ctx, raw)
	if err != nil {
		return rpc.CreateResponse{}, err
	}
	return out.(rpc.CreateResponse), nil
}

func (r grantRig) sandboxOf(t *testing.T, id string) string {
	t.Helper()
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if s := snap.Lookup("system.sandbox"); s != nil {
		return *s
	}
	return ""
}

func TestTheOwnerCreatesOnLocalAsBefore(t *testing.T) {
	r := newGrantRig(t)
	resp, err := r.create(as("user", strconv.Itoa(os.Getuid())), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := r.backend.Meta(resp.FigaroID); m == nil || m.MintedBy != "user:"+strconv.Itoa(os.Getuid()) {
		t.Fatalf("minted_by %+v", m)
	}
}

// A caller allowed bwrap but not local cannot pick a host directory: under
// bwrap the cwd becomes the read-write bind on this machine.
func TestACwdOnAHostPathSandboxNeedsUseOnLocal(t *testing.T) {
	r := newGrantRig(t)
	before := len(r.backend.ConversationIDs())
	_, err := r.create(as("user", "remote"), "boxed")
	if err == nil || !strings.Contains(err.Error(), "sandbox:local") {
		t.Fatalf("err %v", err)
	}
	if after := len(r.backend.ConversationIDs()); after != before {
		t.Fatalf("a refused birth left %d aria(s) behind", after-before)
	}
}

func TestACallerWithoutUseOnTheSandboxIsRefused(t *testing.T) {
	r := newGrantRig(t)
	_, err := r.create(as("user", "remote"), "mock")
	if err == nil || !strings.Contains(err.Error(), `sandbox "local"`) {
		t.Fatalf("err %v", err)
	}
	if n := len(r.backend.ConversationIDs()); n != 0 {
		t.Fatalf("a refused birth left %d aria(s) behind", n)
	}
}

// A sandbox principal's arias are pinned to its own sandbox. dev-3 names
// no configured box, so the pinned birth fails at the sandbox open, which
// shows both the pin and that the failed birth is cleaned up.
func TestASandboxPrincipalIsPinnedToItsOwnSandbox(t *testing.T) {
	r := newGrantRig(t)
	_, err := r.create(as("sandbox", "dev-3"), "mock")
	if err == nil || !strings.Contains(err.Error(), `unknown provider "dev-3"`) {
		t.Fatalf("err %v", err)
	}
	if n := len(r.backend.ConversationIDs()); n != 0 {
		t.Fatalf("a failed birth left %d aria(s) behind", n)
	}
}

func TestAForkBySandboxOfALocalAriaIsPinnedToTheSandbox(t *testing.T) {
	r := newGrantRig(t)
	parent, err := r.create(as("user", strconv.Itoa(os.Getuid())), "mock")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: parent.FigaroID})
	out, err := r.h[rpc.MethodFork](as("sandbox", "dev-3"), raw)
	if err != nil {
		t.Fatal(err)
	}
	alt := out.(rpc.ForkResponse).Alternative
	if got := r.sandboxOf(t, alt); got != "dev-3" {
		t.Fatalf("the fork runs in sandbox %q", got)
	}
	if got := r.sandboxOf(t, parent.FigaroID); got != "" {
		t.Fatalf("the parent moved to %q", got)
	}
	m, _ := r.backend.Meta(alt)
	if p, _ := jkauthz.ParsePrincipal(m.MintedBy); p != (jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "dev-3"}) {
		t.Fatalf("minted_by %q", m.MintedBy)
	}
}

func TestOutfitReloadReloadsThePolicyAndKeepsItWhenTheNewOneIsBroken(t *testing.T) {
	r := newGrantRig(t)
	remote := as("user", "remote")
	if _, err := r.create(remote, "mock"); err == nil {
		t.Fatal("remote created on local before the grant")
	}
	widened := strings.ReplaceAll(grantPolicy, "SELF", strconv.Itoa(os.Getuid())) + `
[[bind]]
role = "owner"
principals = ["user:remote"]
`
	if err := os.WriteFile(r.policy, []byte(widened), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodOutfitReload](as("user", strconv.Itoa(os.Getuid())), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.create(remote, "mock"); err != nil {
		t.Fatalf("after the reload: %v", err)
	}
	if err := os.WriteFile(r.policy, []byte("[roles.broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodOutfitReload](as("user", strconv.Itoa(os.Getuid())), nil); err == nil {
		t.Fatal("a broken policy reloaded without complaint")
	}
	if _, err := r.create(remote, "mock"); err != nil {
		t.Fatalf("a failed reload dropped the serving policy: %v", err)
	}
}
