package angelus_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/decide"
)

type countingHost struct {
	srv   *httptest.Server
	hits  atomic.Int32
	bearr atomic.Value
}

func newCountingHost(t *testing.T) *countingHost {
	t.Helper()
	h := &countingHost{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		h.bearr.Store(r.Header.Get("Authorization"))
		http.Error(w, "no", http.StatusTeapot)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

type staticToken string

func (s staticToken) Resolve() (string, error) { return string(s), nil }
func (staticToken) Invalidate(string) error    { return nil }

func decideThrough(legit *countingHost) func(*angelus.ServerConfig) {
	return func(c *angelus.ServerConfig) {
		c.Decider = decide.New(func(string) (decide.Target, error) {
			return decide.Target{BaseURL: legit.srv.URL, Auth: staticToken("provider-credential")}, nil
		})
	}
}

func heldPatch(t *testing.T, kv map[string]any) *form.Patch {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		set[k] = raw
	}
	p := form.Build(form.Snapshot{}, set, nil)
	return &p
}

func keyOn(t *testing.T, r grantRig, id, key string) (json.RawMessage, bool) {
	t.Helper()
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	return snap.Get(key)
}

// Every key here is one the daemon dereferences on the host: a directory it
// writes, a URL it sends a credential to, keys it writes past every check, forms
// it reads, a role it resolves. A box's own aria is the box's to set, so these
// are the keys whose values must come from the owner or the outfits.
func TestABoxCannotSetKeysTheDaemonDereferencesOnTheHost(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "canary")
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	held := map[string]any{
		"system.environment.figaro_wire_dir": canary,
		"system.decide.base_url":             "http://127.0.0.1:1/",
		"system.methods.flip":                map[string]any{"key": "system.sandbox", "options": []string{"local"}},
		"system.studies":                     []string{outside.FigaroID},
		"target-aria":                        outside.FigaroID,
		"system.reaped":                      outside.FigaroID,
		"system.reaped_until":                1,
	}
	c := d.dial(t)
	call := func(method string, req any) error { return c.Call(context.Background(), method, req, nil) }
	onIt := func(method string, req any) error { return boxOnAria(t, c, method, inBox.FigaroID, req, nil) }

	for key, val := range held {
		p := heldPatch(t, map[string]any{key: val})
		for name, err := range map[string]error{
			"set":         onIt(rpc.MethodSet, rpc.SetRequest{Patch: *p}),
			"qua":         onIt(rpc.MethodQua, rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Patch: p}}),
			"cast":        onIt(rpc.MethodCast, rpc.CastRequest{RolePatch: p}),
			"create":      call(rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}, Patch: p}),
			"fork":        call(rpc.MethodFork, rpc.ForkRequest{FigaroID: inBox.FigaroID, Patch: p}),
			"form.create": call(rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: p}),
			"form.bind":   call(rpc.MethodFormBind, rpc.FormBindRequest{Patch: p}),
		} {
			if err == nil || !strings.Contains(err.Error(), "may not set") {
				t.Errorf("%s of %s from the box: %v", name, key, err)
			}
		}
		if v, ok := keyOn(t, d.grantRig, inBox.FigaroID, key); ok {
			t.Errorf("%s landed on the box's aria: %s", key, v)
		}
	}
	nested := heldPatch(t, map[string]any{"system": map[string]any{"environment": map[string]any{"figaro_wire_dir": canary}}})
	if err := onIt(rpc.MethodSet, rpc.SetRequest{Patch: *nested}); err == nil {
		t.Error("the box set the wire dir spelled as a nested object")
	}
	if err := call(rpc.MethodImport, rpc.ImportRequest{Outfit: "x", Form: message.Patch{}}); err == nil || !strings.Contains(err.Error(), "may not import") {
		t.Errorf("import from the box: %v", err)
	}
	if n := len(d.backend.ConversationIDs()); n != 2 {
		t.Fatalf("refused calls left %d arias, want 2", n)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatalf("the canary directory exists: %v", err)
	}

	if err := onIt(rpc.MethodSet, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"mantra": "fine", "system.model": "m2"})}); err != nil {
		t.Fatalf("a key the daemon does not dereference: %v", err)
	}
	ownerSet := heldPatch(t, map[string]any{"system.environment.figaro_wire_dir": canary})
	if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, inBox.FigaroID, rpc.SetRequest{Patch: *ownerSet})); err != nil {
		t.Fatalf("the owner: %v", err)
	}
	if _, ok := keyOn(t, d.grantRig, inBox.FigaroID, "system.environment.figaro_wire_dir"); !ok {
		t.Fatal("canary: the owner's set did not land, so absence above proves nothing")
	}
}

// The base_url a decision posts to is the one host dereference a test can watch
// whole: the owner's value reaches the canary with the provider's credential; the
// box's never does.
func TestABoxCannotPointADecisionAtAHostOfItsChoosing(t *testing.T) {
	legit, canary := newCountingHost(t), newCountingHost(t)
	d := boxDoorRigWith(t, defaultPolicy, "true", decideThrough(legit))
	owned, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	boxed, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"system.decide.provider": "mock", "system.decide.model": "decision-1"}
	for _, id := range []string{owned.FigaroID, boxed.FigaroID} {
		if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: *heldPatch(t, settings)})); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(ctx context.Context, id string) {
		d.h[rpc.MethodDecide](ctx, withAria(t, id, rpc.DecideRequest{FigaroID: id, Question: "go?"}))
	}

	if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, owned.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.decide.base_url": canary.srv.URL})})); err != nil {
		t.Fatal(err)
	}
	ask(asOwner(), owned.FigaroID)
	if canary.hits.Load() != 1 || canary.bearr.Load() != "Bearer provider-credential" {
		t.Fatalf("canary: the owner's base_url was not dereferenced (%d hits, %v), so the box half proves nothing", canary.hits.Load(), canary.bearr.Load())
	}

	c := d.dial(t)
	err = boxOnAria(t, c, rpc.MethodSet, boxed.FigaroID, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.decide.base_url": canary.srv.URL})}, nil)
	if err == nil {
		t.Fatal("the box set system.decide.base_url")
	}
	before := legit.hits.Load()
	boxOnAria(t, c, rpc.MethodDecide, boxed.FigaroID, rpc.DecideRequest{FigaroID: boxed.FigaroID, Question: "go?"}, nil)
	if canary.hits.Load() != 1 {
		t.Fatalf("the box's decision reached its host: %d hits", canary.hits.Load())
	}
	if legit.hits.Load() == before {
		t.Fatal("the box's decision went nowhere, so the canary's silence proves nothing")
	}
}

// A box aria's system.cwd is a path in the box. Releasing workspaces deletes
// directories under workspace_root that the last aria naming them leaves, so a
// box's value must never name one.
func TestKillingABoxAriaNeverReleasesAHostWorkspace(t *testing.T) {
	d := boxDoorRig(t, "true")
	victim := filepath.Join(filepath.Dir(d.sb.sock), "workspaces", "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	var made rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: victim}, &made); err != nil {
		t.Fatal(err)
	}
	if v, ok := keyOn(t, d.grantRig, made.FigaroID, "system.cwd"); ok {
		t.Fatalf("the host cwd the box sent became system.cwd %s", v)
	}
	if err := setKey(t, c, made.FigaroID, "system.cwd", victim); err != nil {
		t.Fatalf("a box aria's cwd is the box's: %v", err)
	}
	if err := c.Call(context.Background(), rpc.MethodKill, rpc.KillRequest{FigaroID: made.FigaroID, Now: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim, "keep")); err != nil {
		t.Fatalf("killing a box aria released a host workspace: %v", err)
	}
}

// The rule is an allowlist: a system key no one has classified yet is the
// owner's until someone lists it, and the open keys a box writes stay its own.
func TestABoxWritesOnlyListedSystemKeys(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	onIt := func(method string, req any) error { return boxOnAria(t, c, method, inBox.FigaroID, req, nil) }
	if err := onIt(rpc.MethodSet, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{"system.added_tomorrow": "/etc"})}); err == nil || !strings.Contains(err.Error(), "only the system keys it is listed for") {
		t.Fatalf("an unlisted system key: %v", err)
	}
	for key, val := range map[string]any{"system.model": "m2", "system.credo": map[string]any{"content": "be brief"}, "system.tags": map[string]any{"3": map[string]any{"cache_control": "1h"}}, "chalkboard.note": "mine", "mantra": "boxed"} {
		if err := onIt(rpc.MethodSet, rpc.SetRequest{Patch: *heldPatch(t, map[string]any{key: val})}); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
	ctxOnly := &rpc.FormInput{Context: map[string]json.RawMessage{"target-aria": json.RawMessage(`"elsewhere"`)}}
	if err := onIt(rpc.MethodQua, rpc.QuaRequest{Text: "hi", Form: ctxOnly}); err == nil || !strings.Contains(err.Error(), "target-aria") {
		t.Fatalf("target-aria through a prompt's context: %v", err)
	}
	if v, ok := keyOn(t, d.grantRig, inBox.FigaroID, "target-aria"); ok {
		t.Fatalf("target-aria landed: %s", v)
	}
}
