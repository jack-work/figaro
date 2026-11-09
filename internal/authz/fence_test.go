package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const fencedPolicy = sandboxPolicy + `
[[fence]]
verbs = ["send", "read"]
on = ["aria:aaaa0002"]
allow = ["figaro:dddd0001", "figaro:@herald", "figaro:self"]

[[fence]]
verbs = ["kill"]
on = ["*"]
deny = ["figaro:*"]
`

// The three stamps a fence must tell apart: the allowed figaro, the one the
// @herald role points at, and a stranger.
const (
	figY      = "dddd0001"
	figHerald = "bbbb0001"
	figZ      = "eeee0001"
)

func TestAPolicyWithoutFencesLetsEveryStampedFigaroDoWhatTheOwnerMay(t *testing.T) {
	p := loaded(t, sandboxPolicy)
	for _, v := range rpc.Verbs() {
		if d := p.AllowFigaro(subject(me), figZ, v, Aria("aaaa0002")); !d.Allowed {
			t.Fatalf("%s: %s", v, d.Reason())
		}
	}
}

func TestAFenceLetsOnlyTheFigarosItNames(t *testing.T) {
	p := loaded(t, fencedPolicy)
	x := Aria("aaaa0002")
	cases := []struct {
		name   string
		figaro string
		verb   rpc.Verb
		r      Resource
		want   bool
	}{
		{"named figaro reads", figY, rpc.VerbRead, x, true},
		{"named figaro sends", figY, rpc.VerbSend, x, true},
		{"role holder sends", figHerald, rpc.VerbSend, x, true},
		{"the aria itself reads", "aaaa0002", rpc.VerbRead, x, true},
		{"stranger reads", figZ, rpc.VerbRead, x, false},
		{"stranger sends", figZ, rpc.VerbSend, x, false},
		{"stranger sets, which no fence covers", figZ, rpc.VerbSet, x, true},
		{"stranger reads another aria", figZ, rpc.VerbRead, Aria("aaaa0001"), true},
		{"unstamped owner reads", "", rpc.VerbRead, x, true},
		{"unstamped owner kills", "", rpc.VerbKill, x, true},
		{"named figaro kills", figY, rpc.VerbKill, x, false},
	}
	for _, c := range cases {
		d := p.AllowFigaro(subject(me), c.figaro, c.verb, c.r)
		if d.Allowed != c.want {
			t.Errorf("%s: allowed=%v (%s), want %v", c.name, d.Allowed, d.Reason(), c.want)
		}
		if !d.Allowed && (!d.Fenced() || !strings.Contains(d.Reason(), "figaro:"+c.figaro)) {
			t.Errorf("%s: refusal does not name the figaro or the fence: %q", c.name, d.Reason())
		}
	}
}

func TestARoleNameFollowsItsTarget(t *testing.T) {
	f := facts()
	p := NewPolicies(me, f)
	if err := p.load([]byte(fencedPolicy)); err != nil {
		t.Fatal(err)
	}
	if !p.AllowFigaro(subject(me), figHerald, rpc.VerbSend, Aria("aaaa0002")).Allowed {
		t.Fatal("the @herald holder was refused")
	}
	f.roles["@herald"] = figZ
	if p.AllowFigaro(subject(me), figHerald, rpc.VerbSend, Aria("aaaa0002")).Allowed {
		t.Fatal("the role moved on, but its old holder still passes")
	}
	if !p.AllowFigaro(subject(me), figZ, rpc.VerbSend, Aria("aaaa0002")).Allowed {
		t.Fatal("the role's new holder was refused")
	}
}

// The stamp rides on the owner's connection; any other principal is decided
// by its own grants, and a stamp it asserts is attribution only.
func TestFencesBindOnlyTheOwnersPrincipal(t *testing.T) {
	p := loaded(t, fencedPolicy)
	if !p.AllowFigaro(subject(box), figZ, rpc.VerbRead, Aria("aaaa0001")).Allowed {
		t.Fatal("a box's own grant was fenced by a stamp it asserted")
	}
	if p.AllowFigaro(subject(box), figY, rpc.VerbRead, Aria("aaaa0002")).Allowed {
		t.Fatal("a stamp lent a box what its grants do not")
	}
}

func TestAPolicyReloadedWithoutFencesDropsThem(t *testing.T) {
	p := loaded(t, fencedPolicy)
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	if d := p.AllowFigaro(subject(me), figZ, rpc.VerbRead, Aria("aaaa0002")); !d.Allowed {
		t.Fatalf("a removed fence still refuses: %s", d.Reason())
	}
}

func TestMalformedFencesRefuseTheWholePolicy(t *testing.T) {
	for name, fence := range map[string]string{
		"no names":       `verbs = ["read"]` + "\n" + `on = ["*"]`,
		"allow and deny": `verbs = ["read"]` + "\n" + `on = ["*"]` + "\n" + `allow = ["figaro:*"]` + "\n" + `deny = ["figaro:*"]`,
		"no verbs":       `on = ["*"]` + "\n" + `deny = ["figaro:*"]`,
		"unknown verb":   `verbs = ["shave"]` + "\n" + `on = ["*"]` + "\n" + `deny = ["figaro:*"]`,
		"bad selector":   `verbs = ["read"]` + "\n" + `on = ["moon:1"]` + "\n" + `deny = ["figaro:*"]`,
		"a user":         `verbs = ["read"]` + "\n" + `on = ["*"]` + "\n" + `deny = ["user:1000"]`,
		"empty role":     `verbs = ["read"]` + "\n" + `on = ["*"]` + "\n" + `deny = ["figaro:@"]`,
		"bad id":         `verbs = ["read"]` + "\n" + `on = ["*"]` + "\n" + `deny = ["figaro:../x"]`,
		"unknown key":    `verbs = ["read"]` + "\n" + `on = ["*"]` + "\n" + `deny = ["figaro:*"]` + "\n" + `except = ["figaro:*"]`,
	} {
		path := filepath.Join(t.TempDir(), "policy.toml")
		if err := os.WriteFile(path, []byte(sandboxPolicy+"\n[[fence]]\n"+fence+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := NewPolicies(me, facts())
		if err := p.Reload(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestAFencedPolicyStillGrantsWhatItsRolesSay(t *testing.T) {
	p := loaded(t, fencedPolicy)
	if !p.Allow(subject(box), rpc.VerbUse, Sandbox("dev-3")).Allowed {
		t.Fatal("splitting out the fences lost a grant")
	}
	if p.Allow(subject(other), rpc.VerbRead, Aria("aaaa0002")).Allowed {
		t.Fatal("splitting out the fences granted a stranger")
	}
}

func stamped(t *testing.T, aria, figaro string) json.RawMessage {
	t.Helper()
	raw, err := rpc.WithCaller(map[string]string{"figaro_id": aria}, figaro, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTheGuardRefusesAFencedFigaroAndLogsOneLine(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := loaded(t, fencedPolicy)
	reached := 0
	ok := func(context.Context, json.RawMessage) (any, error) { reached++; return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: ok, rpc.MethodIR: ok, rpc.MethodSet: ok}, AriaHeader{}, Enforce(p))
	ctx := WithSubject(context.Background(), subject(me))

	if _, err := h[rpc.MethodQua](ctx, stamped(t, "aaaa0002", figY)); err != nil {
		t.Fatalf("the named figaro: %v", err)
	}
	if _, err := h[rpc.MethodQua](ctx, json.RawMessage(`{"figaro_id":"aaaa0002"}`)); err != nil {
		t.Fatalf("the unstamped owner: %v", err)
	}
	if _, err := h[rpc.MethodSet](ctx, stamped(t, "aaaa0002", figZ)); err != nil {
		t.Fatalf("an unfenced verb: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("allowed calls logged:\n%s", logs.String())
	}

	_, err := h[rpc.MethodIR](ctx, stamped(t, "aaaa0002", figZ))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a fenced read: %v, want the aria hidden", err)
	}
	if n := strings.Count(logs.String(), "authz: fence refused"); n != 1 {
		t.Fatalf("a refusal logged %d lines:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "figaro="+figZ) {
		t.Fatalf("the log line does not name the figaro:\n%s", logs.String())
	}
	if reached != 3 {
		t.Fatalf("handler reached %d times, want 3", reached)
	}
}

func TestTheGuardNamesTheFenceWhenTheFigaroMayStillRead(t *testing.T) {
	p := loaded(t, fencedPolicy)
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodKill: ok}, AriaHeader{}, Enforce(p))
	ctx := WithSubject(context.Background(), subject(me))
	_, err := h[rpc.MethodKill](ctx, stamped(t, "aaaa0001", figY))
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) || jerr.Code != ErrCode || !strings.Contains(jerr.Message, "fence 2") {
		t.Fatalf("kill by a stamped figaro: %v, want -32020 naming fence 2", err)
	}
}

// The grants a policy holds must not move when fences are added to it: the
// fences are cut out of the text jkauthz reads, so a cut that ate a header
// or a line of grants would change decisions without a load error.
func TestFencesLeaveEveryGrantDecisionAsItWas(t *testing.T) {
	spain, err := os.ReadFile("testdata/spain-policy.toml")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"spain":            string(spain),
		"gluck (no file)":  DefaultPolicy(me, nil),
		"default with box": DefaultPolicy(me, []string{"dev-3"}),
		"test":             sandboxPolicy,
	}
	fences := map[string]func(string) string{
		"appended": func(p string) string {
			return p + "\n[[fence]]\nverbs = [\"read\"]\non = [\"*\"]\ndeny = [\"figaro:*\"]\n"
		},
		"first, commented, multi-line": func(p string) string {
			return "# fences\n[[fence]] # one\nverbs = [\n  \"send\",\n  \"kill\",\n]\non = [\"aria:aaaa0002\"]\nallow = [\"figaro:@herald\"]\n\n" + p
		},
		"between every table": func(p string) string {
			return strings.ReplaceAll(p, "\n[", "\n[[fence]]\nverbs = [\"set\"]\non = [\"role:@herald\"]\ndeny = [\"figaro:self\"]\n\n[")
		},
	}
	f := facts()
	f.boxed = map[string]string{"aaaa0001": "box0", "aaaa0003": "dev-3"}
	principals := []string{"user:2001", "user:admin@auth.kelliher.info", "user:1000", "user:1001",
		"sandbox:box0@local:box", "sandbox:chat@local:box", "sandbox:dev-3", "sandbox:dev-3@local:box",
		"troupe:kelliher", "node:n1", "user:admin@evil.example"}
	resources := []Resource{Daemon, Aria("aaaa0001"), Aria("aaaa0002"), Aria("aaaa0003"), Aria("bbbb0001"),
		Aria("cccc0003"), Sandbox("local"), Sandbox("box0"), Sandbox("dev-3"), Troupe("kelliher")}
	for fname, policy := range fixtures {
		plain := NewPolicies(me, f)
		if err := plain.load([]byte(policy)); err != nil {
			t.Fatalf("%s: %v", fname, err)
		}
		for shape, fence := range fences {
			fenced := NewPolicies(me, f)
			if err := fenced.load([]byte(fence(policy))); err != nil {
				t.Fatalf("%s, fences %s: %v", fname, shape, err)
			}
			if fenced.fences.Load() == nil {
				t.Fatalf("%s, fences %s: no fence loaded", fname, shape)
			}
			for _, name := range principals {
				pr, err := jkauthz.ParsePrincipal(name)
				if err != nil {
					t.Fatal(err)
				}
				s := subject(pr)
				if plain.HoldsRole(s) != fenced.HoldsRole(s) {
					t.Errorf("%s, fences %s: %s holds a role in one and not the other", fname, shape, name)
				}
				for _, v := range rpc.Verbs() {
					for _, r := range resources {
						if a, b := plain.Allow(s, v, r).Allowed, fenced.Allow(s, v, r).Allowed; a != b {
							t.Errorf("%s, fences %s: %s %s on %s: %v without fences, %v with", fname, shape, name, v, r, a, b)
						}
					}
				}
			}
		}
	}
}

func TestAFenceTheCutCannotSetApartRefusesThePolicy(t *testing.T) {
	for name, src := range map[string]string{
		"inline":        "fence = [{ verbs = [\"read\"], on = [\"*\"], deny = [\"figaro:*\"] }]\n" + sandboxPolicy,
		"quoted header": sandboxPolicy + "\n[[\"fence\"]]\nverbs = [\"read\"]\non = [\"*\"]\ndeny = [\"figaro:*\"]\n",
	} {
		p := NewPolicies(me, facts())
		err := p.load([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "[[fence]] table of its own") {
			t.Errorf("%s: %v, want a refusal asking for [[fence]] tables", name, err)
		}
	}
}

func TestTheStampReachesTheHandlerContext(t *testing.T) {
	p := loaded(t, sandboxPolicy)
	var inner context.Context
	h := Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodQua: func(ctx context.Context, _ json.RawMessage) (any, error) { inner = ctx; return "ok", nil },
	}, AriaHeader{}, Enforce(p))
	ctx := WithSubject(context.Background(), subject(me))
	if _, err := Authenticated(AriaHeader{}, h[rpc.MethodQua])(ctx, stamped(t, "aaaa0002", figY)); err != nil {
		t.Fatal(err)
	}
	if got := FigaroOf(inner); got != figY {
		t.Fatalf("handler saw %q, want %q", got, figY)
	}
	if got := FigaroOf(WithSubject(inner, subject(other))); got != "" {
		t.Fatalf("another principal inherited the stamp %q", got)
	}
}
