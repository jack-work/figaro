package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

func narrowed(t *testing.T, policy string, own map[string]string) *Policies {
	t.Helper()
	f := lineageFakes(own)
	f.owners["kid"] = me
	p := NewPolicies(me, f)
	if err := p.load([]byte(policy)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAFigaroCallIsTheCallersGrantsIntersectedWithTheFigaros(t *testing.T) {
	p := narrowed(t, sandboxPolicy, map[string]string{"top": `["read:*", "send:aria:aaaa0002", "kill:lineage:self"]`})
	for _, c := range []struct {
		name   string
		figaro string
		verb   rpc.Verb
		on     Resource
		want   bool
	}{
		{"read anything", "kid", rpc.VerbRead, Aria("aaaa0001"), true},
		{"send where granted", "kid", rpc.VerbSend, Aria("aaaa0002"), true},
		{"send elsewhere", "kid", rpc.VerbSend, Aria("aaaa0001"), false},
		{"kill inside the top's lineage", "kid", rpc.VerbKill, Aria("grandkid"), true},
		{"kill outside it", "kid", rpc.VerbKill, Aria("aaaa0001"), false},
		{"admin the daemon", "kid", rpc.VerbAdmin, Daemon, false},
		{"no claim: the principal alone", "", rpc.VerbAdmin, Daemon, true},
		{"a figaro not held here", "elsewhere", rpc.VerbRead, Aria("aaaa0001"), false},
	} {
		d := p.AllowFigaro(subject(me), c.figaro, c.verb, c.on)
		if d.Allowed != c.want {
			t.Errorf("%s: allowed=%v (%s)", c.name, d.Allowed, d.Reason())
		}
		if !d.Allowed && (!d.Narrowed() || !strings.Contains(d.Reason(), "figaro:"+c.figaro)) {
			t.Errorf("%s: the refusal does not name the figaro: %s", c.name, d.Reason())
		}
	}
	if d := p.AllowFigaro(subject(other), "kid", rpc.VerbRead, Aria("aaaa0001")); d.Allowed {
		t.Error("a figaro's grants widened a principal that holds no role")
	}
}

func TestTheCeilingBoundsEveryTopLevelFigaro(t *testing.T) {
	p := narrowed(t, sandboxPolicy+"\n[figaros]\npermissions = [\"read:*\", \"*:lineage:self\", \"egress:*\"]\n", map[string]string{"top": `["*:*", "egress:api:443", "cred:github"]`})
	top, _ := p.Effective("top")
	if got := top.String(); got != "*:lineage:top, read:*, egress:api:443" {
		t.Fatalf("top: %s", got)
	}
	kid, _ := p.Effective("kid")
	if got := kid.String(); got != "*:lineage:top, read:*" {
		t.Fatalf("kid: %s", got)
	}
	if d := p.AllowFigaro(subject(me), "top", rpc.VerbSend, Aria("aaaa0002")); d.Allowed {
		t.Error("the ceiling let a top-level figaro send outside its lineage")
	}
	if d := p.AllowFigaro(subject(me), "top", rpc.VerbKill, Aria("grandkid")); !d.Allowed {
		t.Errorf("lineage:self: %s", d.Reason())
	}
	if d := p.Allow(subject(me), rpc.VerbSend, Aria("aaaa0002")); !d.Allowed {
		t.Error("the ceiling narrowed an unclaimed call")
	}
}

func TestTheCeilingTableIsChecked(t *testing.T) {
	for name, table := range map[string]string{
		"no grants":    "[figaros]\n",
		"unknown key":  "[figaros]\npermissions = [\"read:*\"]\nverbs = [\"read\"]\n",
		"bad grant":    "[figaros]\npermissions = [\"read\"]\n",
		"bad selector": "[figaros]\npermissions = [\"read:aria:*\"]\n",
		"not a table":  "figaros = 1\n",
	} {
		p := NewPolicies(me, facts())
		if err := p.load([]byte(sandboxPolicy + "\n" + table)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	p := NewPolicies(me, facts())
	if err := p.load([]byte(sandboxPolicy + "\n[figaros]\npermissions = []\n")); err != nil {
		t.Fatalf("an empty ceiling: %v", err)
	}
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	if _, set := p.ceiling(); set {
		t.Fatal("a reload without [figaros] kept the old ceiling")
	}
}

func boxClaimRig(t *testing.T, endpoint Endpoint) (func(figaro string, method string, on string) (string, error), *Policies) {
	t.Helper()
	f := lineageFakes(map[string]string{"in-judge": `["read:*"]`})
	f.boxed = map[string]string{"in-judge": "judge", "in-other": "other", "local-one": "local"}
	for _, id := range []string{"in-judge", "in-other", "local-one"} {
		f.chains[id] = []string{id}
	}
	p := NewPolicies(me, f)
	if err := p.load([]byte(DefaultPolicy(me, []string{"judge"}))); err != nil {
		t.Fatal(err)
	}
	var seen string
	ok := func(ctx context.Context, _ json.RawMessage) (any, error) { seen = FigaroOf(ctx); return nil, nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodIR: ok, rpc.MethodKill: ok}, AriaHeader{}, Enforce(p))
	ctx := WithEndpoint(WithSubject(context.Background(), jkauthz.Subject{Principal: BoxPrincipal("judge")}), endpoint)
	return func(figaro, method, on string) (string, error) {
		seen = "unreached"
		params, _ := json.Marshal(map[string]any{"figaro_id": on})
		if figaro != "" {
			params = stamped(t, on, figaro)
		}
		_, err := Authenticated(AriaHeader{}, h[method])(ctx, params)
		return seen, err
	}, p
}

func TestABoxClaimCountsOnlyForAFigaroInThatBox(t *testing.T) {
	call, p := boxClaimRig(t, EndpointBox)
	if seen, err := call("made-up", rpc.MethodKill, "in-judge"); err != nil || seen != "" {
		t.Fatalf("a made-up claim must fall back to the box: seen %q, %v", seen, err)
	}
	if seen, err := call("in-other", rpc.MethodKill, "in-judge"); err != nil || seen != "" {
		t.Fatalf("another box's figaro: seen %q, %v", seen, err)
	}
	if _, err := call("in-judge", rpc.MethodKill, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro:in-judge") {
		t.Fatalf("the box's own figaro is narrowed by its grants: %v", err)
	}
	if seen, err := call("in-judge", rpc.MethodIR, "in-judge"); err != nil || seen != "in-judge" {
		t.Fatalf("the box's own figaro reads: seen %q, %v", seen, err)
	}

	p.SetBoxesRequiringClaim([]string{"other"})
	if _, err := call("", rpc.MethodIR, "in-judge"); err != nil {
		t.Fatalf("another box's requirement reached this one: %v", err)
	}
	p.SetBoxesRequiringClaim([]string{"judge"})
	for _, figaro := range []string{"", "made-up", "in-other", "local-one"} {
		if _, err := call(figaro, rpc.MethodIR, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro_claim") {
			t.Errorf("required, claim %q: %v", figaro, err)
		}
	}
	if _, err := call("in-judge", rpc.MethodIR, "in-judge"); err != nil {
		t.Fatalf("required, own figaro: %v", err)
	}
	if _, err := call("in-judge", rpc.MethodKill, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro:in-judge") {
		t.Fatalf("required, own figaro, narrowed: %v", err)
	}
}

func TestAHostEndpointIsAnsweredAsTheBoxWhateverTheBoxRequires(t *testing.T) {
	call, p := boxClaimRig(t, EndpointHost)
	p.SetBoxesRequiringClaim([]string{"judge"})
	if seen, err := call("", rpc.MethodKill, "in-judge"); err != nil || seen != "" {
		t.Fatalf("an unclaimed host-door call to a required box: seen %q, %v", seen, err)
	}
	if seen, err := call("made-up", rpc.MethodKill, "in-judge"); err != nil || seen != "" {
		t.Fatalf("a made-up claim on the host door must fall back to the box: seen %q, %v", seen, err)
	}
	if _, err := call("in-judge", rpc.MethodKill, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro:in-judge") {
		t.Fatalf("a claim of the box's own figaro on the host door is narrowed: %v", err)
	}
	inBox, q := boxClaimRig(t, EndpointBox)
	q.SetBoxesRequiringClaim([]string{"judge"})
	if _, err := inBox("", rpc.MethodKill, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro_claim") {
		t.Fatalf("the same unclaimed call in the box: %v", err)
	}
}

func TestTheClaimRequirementStaysOnTheBoxEndpoint(t *testing.T) {
	p := NewPolicies(me, facts())
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	p.SetBoxesRequiringClaim([]string{"dev-3"})
	ok := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	call := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: ok}, AriaHeader{}, Enforce(p))[rpc.MethodQua]
	plain, _ := json.Marshal(map[string]any{"figaro_id": "aaaa0001"})
	if _, err := call(WithSubject(context.Background(), subject(me)), plain); err != nil {
		t.Fatalf("the requirement reached another door: %v", err)
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

func TestTheGuardRefusesANarrowedFigaroAndLogsOneLine(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := narrowed(t, sandboxPolicy, map[string]string{"kid": `["set:*", "read:aria:aaaa0001"]`})
	reached := 0
	ok := func(context.Context, json.RawMessage) (any, error) { reached++; return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: ok, rpc.MethodIR: ok, rpc.MethodSet: ok}, AriaHeader{}, Enforce(p))
	ctx := WithSubject(context.Background(), subject(me))

	if _, err := h[rpc.MethodQua](ctx, stamped(t, "aaaa0002", "top")); err != nil {
		t.Fatalf("a figaro with no grants: %v", err)
	}
	if _, err := h[rpc.MethodQua](ctx, json.RawMessage(`{"figaro_id":"aaaa0002"}`)); err != nil {
		t.Fatalf("the unstamped owner: %v", err)
	}
	if _, err := h[rpc.MethodSet](ctx, stamped(t, "aaaa0002", "kid")); err != nil {
		t.Fatalf("a granted verb: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("allowed calls logged:\n%s", logs.String())
	}
	_, err := h[rpc.MethodIR](ctx, stamped(t, "aaaa0002", "kid"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a read the figaro's grants refuse: %v, want the aria hidden", err)
	}
	if n := strings.Count(logs.String(), "authz: figaro refused"); n != 1 {
		t.Fatalf("a refusal logged %d lines:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "figaro=kid") {
		t.Fatalf("the log line does not name the figaro:\n%s", logs.String())
	}
	if reached != 3 {
		t.Fatalf("handler reached %d times, want 3", reached)
	}
}

func TestTheGuardNamesTheFigaroWhenItMayStillRead(t *testing.T) {
	p := narrowed(t, sandboxPolicy, map[string]string{"kid": `["read:*"]`})
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodKill: ok}, AriaHeader{}, Enforce(p))
	ctx := WithSubject(context.Background(), subject(me))
	_, err := h[rpc.MethodKill](ctx, stamped(t, "aaaa0001", "kid"))
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) || jerr.Code != ErrCode || !strings.Contains(jerr.Message, "figaro:kid") || !strings.Contains(jerr.Message, "read:*") {
		t.Fatalf("kill by a figaro holding read:*: %v, want -32020 naming the figaro and its grants", err)
	}
}

func TestTheStampReachesTheHandlerContext(t *testing.T) {
	p := narrowed(t, sandboxPolicy, nil)
	var inner context.Context
	h := Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodQua: func(ctx context.Context, _ json.RawMessage) (any, error) { inner = ctx; return "ok", nil },
	}, AriaHeader{}, Enforce(p))
	ctx := WithSubject(context.Background(), subject(me))
	if _, err := Authenticated(AriaHeader{}, h[rpc.MethodQua])(ctx, stamped(t, "aaaa0002", "kid")); err != nil {
		t.Fatal(err)
	}
	if got := FigaroOf(inner); got != "kid" {
		t.Fatalf("handler saw %q, want kid", got)
	}
	if got := FigaroOf(WithSubject(inner, subject(other))); got != "" {
		t.Fatalf("another principal inherited the stamp %q", got)
	}
}

func TestAPolicyWithFencesIsRefusedNamingTheReplacement(t *testing.T) {
	p := NewPolicies(me, facts())
	err := p.load([]byte(sandboxPolicy + "\n[[fence]]\ngrants = [\"kill:*\"]\ndeny = [\"figaro:*\"]\n"))
	if err == nil || !strings.Contains(err.Error(), "system.scopes") || !strings.Contains(err.Error(), "[figaros]") {
		t.Fatalf("%v", err)
	}
}

// El Inspector XV: an endpoint drops a claim for a figaro it does not hold, and
// says so once with the endpoint and the id, so a process outliving its killed
// aria shows up rather than silently going wider. The params stay out.
func TestAEndpointDroppingAClaimNotHeldLogsTheEndpointAndTheID(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := narrowed(t, sandboxPolicy, nil)
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: func(context.Context, json.RawMessage) (any, error) { return "ok", nil }}, AriaHeader{}, Enforce(p))
	params, err := rpc.WithCaller(map[string]string{"figaro_id": "aaaa0002", "text": "PARAMS-STAY-OUT"}, "elsewhere", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []Endpoint{EndpointLocal, EndpointNode, EndpointWeb} {
		logs.Reset()
		if _, err := h[rpc.MethodQua](WithEndpoint(WithSubject(context.Background(), subject(me)), d), params); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		line := logs.String()
		if strings.Count(line, "claim for a figaro not held here dropped") != 1 || !strings.Contains(line, "door="+d.String()) || !strings.Contains(line, "figaro=elsewhere") {
			t.Errorf("%s: want one drop line naming the door and the id:\n%s", d, line)
		}
		if strings.Contains(line, "PARAMS-STAY-OUT") {
			t.Errorf("%s: the drop line carries the params:\n%s", d, line)
		}
	}
	logs.Reset()
	if _, err := h[rpc.MethodQua](WithEndpoint(WithSubject(context.Background(), subject(me)), EndpointInProcess), params); err == nil {
		t.Fatal("in-process: a claim not held here ran")
	}
	if strings.Contains(logs.String(), "dropped") {
		t.Errorf("in-process: a refused claim logged as dropped:\n%s", logs.String())
	}
}
