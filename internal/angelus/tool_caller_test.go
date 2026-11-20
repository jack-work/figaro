package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/store"
)

func toolOf(figaro string) context.Context {
	return authn.WithToolOf(localOwner(), figaro)
}

func (r licenseRig) setFrom(ctx context.Context, t *testing.T, target string, kv map[string]any) error {
	t.Helper()
	_, err := r.h[rpc.MethodSet](ctx, withAria(t, target, rpc.SetRequest{Patch: *heldPatch(t, kv)}))
	return err
}

// The peer check's contract at the handlers: a call from a figaro's tool
// cgroup is that figaro whatever it claims, and is never the owner.
func TestAToolCallerIsItsFigaroAndNeverTheOwner(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	other := ariasFork(t, r.grantRig, r.root)

	err := r.setFrom(toolOf(r.x), t, other, map[string]any{"system.model": "x"})
	if err == nil || !strings.Contains(err.Error(), "figaro "+r.x+" may not set system.model") {
		t.Fatalf("an unlabelled tool caller set an owner-held key: %v", err)
	}
	_, err = r.h[rpc.MethodSet](toolOf(r.x), claimed(t, other, withAria(t, other, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.model": "x"})})))
	if err == nil || !strings.Contains(err.Error(), "figaro "+r.x) {
		t.Fatalf("a tool caller claiming another figaro was taken for it: %v", err)
	}
	if err := r.setFrom(toolOf(r.x), t, other, map[string]any{"mantra": "fine"}); err != nil {
		t.Fatalf("a tool caller lost an open key: %v", err)
	}
	if err := r.setFrom(localOwner(), t, other, map[string]any{"system.model": "x"}); err != nil {
		t.Fatalf("the owner at a terminal was refused: %v", err)
	}
	if v, _ := keyOn(t, r.grantRig, other, "system.model"); string(v) != `"x"` {
		t.Fatalf("system.model = %s", v)
	}
}

func TestAToolCallerSetsProtectedKeysOnlyOnItsDescendants(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	kid := ariasFork(t, r.grantRig, r.x)
	if err := r.setFrom(toolOf(r.x), t, kid, map[string]any{form.FanoutMaxChildrenKey: 0}); err != nil {
		t.Fatalf("an ancestor's tool could not narrow its child: %v", err)
	}
	err := r.setFrom(toolOf(kid), t, r.x, map[string]any{form.FanoutMaxChildrenKey: 9})
	if err == nil || !strings.Contains(err.Error(), "figaro "+kid+" may not set "+form.FanoutMaxChildrenKey) {
		t.Fatalf("a child's tool set a protected key on its parent: %v", err)
	}
}

func TestAToolCallerSendsUnderItsOwnName(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{rpc.CallerLabelKey: rpc.CallerRef{Duke: true}})
	var seen json.RawMessage
	h := authn.Authenticated(authn.AriaHeader{}, func(_ context.Context, p json.RawMessage) (any, error) {
		seen = p
		return nil, nil
	})
	if _, err := h(toolOf("ab12"), raw); err != nil {
		t.Fatal(err)
	}
	if got := rpc.SenderFrom(seen, func() string { return "Gluck" }); got != "ab12" {
		t.Fatalf("a tool caller asserting the duke is sent as %q", got)
	}
}

func TestAToolCallerGetsNoOperatorPathsInManagedApply(t *testing.T) {
	r, id := managedRole(t)
	fanoutOutfits(t, r)
	managed := []string{form.FanoutMaxChildrenKey}
	if err := applyFanoutAs(t, r, toolOf("ab12ab12ab12ab12ab12ab12ab12ab12"), id, "fanout", managed, ""); err == nil {
		t.Fatal("a tool caller with no claim applied the operator's system.fanout")
	}
	if _, ok := keyOn(t, r, id, form.FanoutMaxChildrenKey); ok {
		t.Fatal("max_children landed from a tool caller")
	}
	if err := applyFanout(t, r, id, "fanout", managed, ""); err != nil {
		t.Fatalf("the owner lost the operator path: %v", err)
	}
}

// A tool shell may shorten a lifetime, on itself, a descendant or a figaro it
// forks, since that only bounds what it already commands. Making one longer
// or endless stays the owner's.
func TestAToolCallerOnlyShortensLifetimes(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	kid := ariasFork(t, r.grantRig, r.x)
	other := ariasFork(t, r.grantRig, r.root)
	tool := toolOf(r.x)
	ttl := func(id string) string {
		v, _ := keyOn(t, r.grantRig, id, store.SystemTTLKey)
		return string(v)
	}

	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: r.x, Patch: heldPatch(t, map[string]any{store.SystemTTLKey: "2h"})})
	out, err := r.h[rpc.MethodFork](tool, raw)
	if err != nil {
		t.Fatalf("fork -S system.ttl=2h from a tool shell: %v", err)
	}
	if got := ttl(out.(rpc.ForkResponse).Alternative); got != `"2h"` {
		t.Fatalf("the fork's system.ttl = %s, want \"2h\"", got)
	}

	for _, step := range []struct {
		on, value string
		ok        bool
	}{
		{kid, "0", false},
		{kid, "1h", true},
		{kid, "2h", false},
		{kid, "30m", true},
		{other, "30m", false},
		{r.x, "12h", true},
		{r.x, "0", false},
	} {
		err := r.setFrom(tool, t, step.on, map[string]any{store.SystemTTLKey: step.value})
		if step.ok && err != nil {
			t.Errorf("system.ttl %s on %s: %v", step.value, step.on, err)
		}
		if !step.ok && (err == nil || !strings.Contains(err.Error(), "only shorten a lifetime")) {
			t.Errorf("system.ttl %s on %s was not refused as a lengthening: %v", step.value, step.on, err)
		}
	}
	if got := ttl(kid); got != `"30m"` {
		t.Fatalf("the child's system.ttl = %s, want \"30m\"", got)
	}

	if err := r.setFrom(tool, t, kid, map[string]any{store.SystemRetiredKey: store.RetiredManual, store.SystemTTLKey: "20m"}); err != nil {
		t.Fatalf("retire --ttl on a child from a tool shell: %v", err)
	}
	unretire := form.Build(form.Snapshot{}, nil, []string{store.SystemRetiredKey})
	if _, err := r.h[rpc.MethodSet](tool, withAria(t, kid, rpc.SetRequest{Patch: unretire})); err == nil {
		t.Fatal("a tool shell unretired its child")
	}
	if err := r.setFrom(localOwner(), t, kid, map[string]any{store.SystemTTLKey: "0"}); err != nil {
		t.Fatalf("the owner could not make the child endless: %v", err)
	}
}
