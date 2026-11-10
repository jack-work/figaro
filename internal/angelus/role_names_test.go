package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

// A role is minted with a name so a person can type it: `figaro send @figaro`
// must reach the role whose name is figaro. The name is looked up only among
// roles the caller may read, matched exactly, and ids and handles still win.

type nameRig struct {
	t     *testing.T
	a     *Angelus
	h     *handlers
	hs    *Handlers
	owner context.Context
	guest context.Context
}

func newNameRig(t *testing.T) *nameRig {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs()}
	return &nameRig{t: t, a: a}
}

func (r *nameRig) aria() string {
	r.t.Helper()
	return mintConversation(r.t, &handlers{angelus: r.a})
}

func (r *nameRig) role(name, target string) string {
	r.t.Helper()
	keys := map[string]json.RawMessage{}
	if name != "" {
		keys["name"], _ = json.Marshal(name)
	}
	if target != "" {
		keys["target-aria"], _ = json.Marshal(target)
	}
	id, _, err := r.a.Backend.CreateForm("", form.Build(form.Snapshot{}, keys, nil))
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

// serve loads a policy in which the guest may read exactly the ids given, and
// only then builds the handlers, so the door guard is the daemon's own.
func (r *nameRig) serve(guestReads ...string) {
	r.t.Helper()
	r.a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, nil)
	on := []string{`"read:aria:nothing-at-all"`}
	for _, id := range guestReads {
		on = append(on, `"read:aria:`+id+`"`)
	}
	policy := filepath.Join(r.t.TempDir(), "policy.toml")
	body := `
[roles.owner]
grants = ["*:*"]
[roles.guest]
grants = [` + strings.Join(on, ", ") + `]
[[bind]]
role = "owner"
principals = ["user:owner"]
[[bind]]
role = "guest"
principals = ["user:guest"]
`
	if err := os.WriteFile(policy, []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := r.a.Policies.Reload(policy); err != nil {
		r.t.Fatal(err)
	}
	r.hs = NewHandlers(ServerConfig{Angelus: r.a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	r.h = r.hs.h
	r.owner = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "owner"})
	r.guest = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "guest"})
}

func (r *nameRig) resolves(ctx context.Context, ref, want string) {
	r.t.Helper()
	got, err := r.h.canonicalID(ctx, ref)
	if err != nil || got != want {
		r.t.Fatalf("canonicalID(%q) = %q, %v; want %q", ref, got, err, want)
	}
}

func (r *nameRig) form(ctx context.Context, ref string) (string, error) {
	r.t.Helper()
	raw, _ := json.Marshal(map[string]any{"figaro_id": ref})
	out, err := r.hs.Map[rpc.MethodForm](ctx, raw)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func TestARoleIsReachedByItsName(t *testing.T) {
	r := newNameRig(t)
	target := r.aria()
	role := r.role("figaro", target)
	r.serve()

	r.resolves(r.owner, "@figaro", role)
	got, err := r.form(r.owner, "@figaro")
	if err != nil || !strings.Contains(got, target) {
		t.Fatalf("form @figaro answered %q, %v; want the role naming %s", got, err, target)
	}
}

func TestANameIsMatchedExactly(t *testing.T) {
	r := newNameRig(t)
	r.role("figaro", r.aria())
	r.serve()

	for _, ref := range []string{"@figa", "@figaro2", "@Figaro"} {
		r.resolves(r.owner, ref, ref)
	}
}

// A form not yet cast is what `figaro cast <aria> @name` names, so it answers
// to its name too, but a role carrying the same name comes first.
func TestAnUncastFormIsReachedByNameAfterRoles(t *testing.T) {
	r := newNameRig(t)
	plain := r.role("plain", "")
	r.role("shared", "")
	role := r.role("shared", r.aria())
	r.serve()

	r.resolves(r.owner, "@plain", plain)
	r.resolves(r.owner, "@shared", role)
}

func TestANameTwoUncastFormsShareIsAnError(t *testing.T) {
	r := newNameRig(t)
	one, two := r.role("twin", ""), r.role("twin", "")
	r.serve()

	_, err := r.h.canonicalID(r.owner, "@twin")
	if err == nil {
		t.Fatal("@twin resolved though two forms carry that name")
	}
	for _, id := range []string{one, two} {
		if !strings.Contains(err.Error(), handle.Words(id, handle.MinWords)) {
			t.Fatalf("the ambiguity does not name %s by its handle: %v", id, err)
		}
	}
}

// The form slot of a cast or a study is resolved before the aria's agent sees
// it; a name nothing carries is refused there, in words that name no trunk,
// and a guest hears the same for a form it may not read.
func TestAFormSlotNamingNothingSaysSo(t *testing.T) {
	r := newNameRig(t)
	aria := r.aria()
	r.role("hidden", "")
	r.serve(aria)

	cast := func(ctx context.Context, ref string) error {
		raw, _ := json.Marshal(map[string]any{"figaro_id": aria, "form_id": ref})
		_, err := r.hs.Map[rpc.MethodCast](ctx, raw)
		return err
	}
	err := cast(r.owner, "@nobody")
	if err == nil || err.Error() != "no role or form named @nobody" {
		t.Fatalf("cast @nobody answered %v, want: no role or form named @nobody", err)
	}
	hidden, missing := cast(r.guest, "@hidden"), cast(r.guest, "@nobody")
	if hidden == nil || missing == nil || strings.ReplaceAll(missing.Error(), "@nobody", "@hidden") != hidden.Error() {
		t.Fatalf("the guest tells a hidden form from a missing one:\n  hidden:  %v\n  missing: %v", hidden, missing)
	}
}

func TestANameTwoRolesShareIsAnErrorNamingBoth(t *testing.T) {
	r := newNameRig(t)
	one, two := r.role("twin", r.aria()), r.role("twin", r.aria())
	r.serve()

	_, err := r.h.canonicalID(r.owner, "@twin")
	if err == nil {
		t.Fatal("@twin resolved though two roles carry that name")
	}
	for _, id := range []string{one, two} {
		if !strings.Contains(err.Error(), handle.Words(id, handle.MinWords)) {
			t.Fatalf("the ambiguity does not name %s by its handle: %v", id, err)
		}
	}
}

func TestIDsAndHandlesWinOverNames(t *testing.T) {
	r := newNameRig(t)
	other := r.role("", r.aria())
	byHandle := strings.TrimPrefix(handle.Words(other, handle.MinWords), "@")
	byPrefix := strings.TrimPrefix(other, "@")[:6]
	r.role(byHandle, r.aria())
	r.role(byPrefix, r.aria())
	r.serve()

	r.resolves(r.owner, "@"+byHandle, other)
	r.resolves(r.owner, "@"+byPrefix, other)
}

// A guest who may not read the role hears what it hears for a name no role
// carries; a guest who may read one of two same-named roles reaches that one,
// and the error that would name the other is never raised.
func TestANameReachesOnlyRolesTheCallerMayRead(t *testing.T) {
	r := newNameRig(t)
	hidden := r.role("secret", r.aria())
	mine, theirs := r.role("shared", r.aria()), r.role("shared", r.aria())
	r.serve(mine)

	got, gotErr := r.form(r.guest, "@secret")
	_, wantErr := r.form(r.guest, "@nobody")
	if gotErr == nil || wantErr == nil {
		t.Fatalf("a guest read a role it may not: %q, %v (baseline %v)", got, gotErr, wantErr)
	}
	if strings.ReplaceAll(wantErr.Error(), "@nobody", "@secret") != gotErr.Error() {
		t.Fatalf("the guest tells a hidden role from a missing one:\n  hidden:  %v\n  missing: %v", gotErr, wantErr)
	}
	if strings.Contains(gotErr.Error(), hidden) || strings.Contains(gotErr.Error(), handle.Words(hidden, handle.MinWords)) {
		t.Fatalf("the refusal names the hidden role: %v", gotErr)
	}

	r.resolves(r.guest, "@shared", mine)
	if _, err := r.h.canonicalID(r.owner, "@shared"); err == nil || !strings.Contains(err.Error(), handle.Words(theirs, handle.MinWords)) {
		t.Fatalf("the owner reads both, so @shared is ambiguous: %v", err)
	}
}

// A name that happens to be hex is still a name when no id answers to it.
func TestAHexLookingNameStillReachesItsRole(t *testing.T) {
	r := newNameRig(t)
	role := r.role("beef", r.aria())
	r.serve()

	r.resolves(r.owner, "@beef", role)
}
