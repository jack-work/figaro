package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/authz"
)

var updateDecisions = flag.Bool("update-decisions", false, "rewrite testdata/decision_equivalence.golden from the decisions made now")

// The decision-equivalence table. Each row is one caller asking one method
// on one resource, and the golden file holds what the daemon answered: ok,
// the refusal it gave, or the rows it showed. A change that should decide
// nothing differently must leave the file byte-identical; a change that
// means to must show its diff here.

type eqWorld struct {
	lineageRig
	box    *jkrpc.Client
	names  map[string]string
	outfit string
}

func eqPolicy() string {
	owner := jkauthz.Principal{Kind: jkauthz.KindUser, ID: strconv.Itoa(os.Getuid())}
	return authz.DefaultPolicy(owner, []string{"judge"}) + `
[roles.reader]
permissions = ["read:*", "listen:*"]

[[bind]]
role = "reader"
principals = ["user:reader"]
`
}

func newEqWorld(t *testing.T, boxExtra string) *eqWorld {
	t.Helper()
	d := boxDoorRigConfigured(t, eqPolicy(), "true", judgeListsAnyOutfit+boxExtra)
	p, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	fork := func() string {
		out, err := d.call(asOwner(), rpc.MethodFork, rpc.ForkRequest{FigaroID: p.FigaroID})
		if err != nil {
			t.Fatal(err)
		}
		return out.(rpc.ForkResponse).Alternative
	}
	r := lineageRig{doorRig: d, parent: p.FigaroID, child: fork(), sibling: fork()}
	if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, r.parent, rpc.SetRequest{Patch: grantsPatch(t, []string{"*:*"})})); err != nil {
		t.Fatal(err)
	}
	if _, err := d.h[rpc.MethodSet](asOwner(), withAria(t, r.child, rpc.SetRequest{Patch: grantsPatch(t, []string{"read:*", "send:parent", "write:aria:self#mantra", "fork:aria:self"})})); err != nil {
		t.Fatal(err)
	}
	out, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	w := &eqWorld{lineageRig: r, box: d.dial(t), outfit: out.FigaroID}
	w.names = map[string]string{r.parent: "P", r.child: "C", r.sibling: "S", out.FigaroID: "O"}
	return w
}

func (w *eqWorld) id(name string) string {
	for id, n := range w.names {
		if n == name {
			return id
		}
	}
	return name
}

type eqCaller struct {
	name     string
	boxExtra string
	claim    string
	viaBox   bool
	shadow   bool
	ctx      func(w *eqWorld) context.Context
}

func principalAt(kind, id string, door authn.Door) func(*eqWorld) context.Context {
	return func(*eqWorld) context.Context {
		return authn.WithDoor(as(kind, id), door)
	}
}

func ownerAt(door authn.Door) func(*eqWorld) context.Context {
	return principalAt("user", strconv.Itoa(os.Getuid()), door)
}

func eqCallers() []eqCaller {
	return []eqCaller{
		{name: "owner@local", ctx: ownerAt(authn.DoorLocal)},
		{name: "owner@in-process", ctx: ownerAt(authn.DoorInProcess)},
		{name: "owner@web", ctx: ownerAt(authn.DoorWeb)},
		{name: "owner@local as P", claim: "P", ctx: ownerAt(authn.DoorLocal)},
		{name: "owner@local as C", claim: "C", ctx: ownerAt(authn.DoorLocal)},
		{name: "owner@local as O", claim: "O", ctx: ownerAt(authn.DoorLocal)},
		{name: "tool of C", ctx: func(w *eqWorld) context.Context {
			return authn.WithToolOf(ownerAt(authn.DoorLocal)(w), w.child)
		}},
		{name: "reader@web", ctx: principalAt("user", "reader", authn.DoorWeb)},
		{name: "stranger@web", ctx: principalAt("user", "stranger", authn.DoorWeb)},
		{name: "box", viaBox: true},
		{name: "box as P", viaBox: true, claim: "P"},
		{name: "box as C", viaBox: true, claim: "C"},
		{name: "box as O", viaBox: true, claim: "O"},
		{name: "box(claim required)", viaBox: true, boxExtra: "figaro_claim = \"required\"\n"},
		{name: "box(claim required) as C", viaBox: true, claim: "C", boxExtra: "figaro_claim = \"required\"\n"},
		{name: "owner@local as C, shadowed", claim: "C", shadow: true, ctx: ownerAt(authn.DoorLocal)},
		{name: "box as C, shadowed", viaBox: true, claim: "C", shadow: true},
		{name: "box, shadowed", viaBox: true, shadow: true},
		{name: "owner@local as C, open", claim: "C", boxExtra: protectedOpen, ctx: ownerAt(authn.DoorLocal)},
		{name: "box as C, open", viaBox: true, claim: "C", boxExtra: protectedOpen},
	}
}

type eqCase struct {
	name   string
	method string
	params func(t *testing.T, w *eqWorld) json.RawMessage
	rows   bool
}

func eqPatch(t *testing.T, kv map[string]any) *form.Patch { return heldPatch(t, kv) }

func onTarget(name, method string, req func(t *testing.T, w *eqWorld) any) func(string) eqCase {
	return func(target string) eqCase {
		return eqCase{name: name + " " + target, method: method, params: func(t *testing.T, w *eqWorld) json.RawMessage {
			return withAria(t, w.id(target), req(t, w))
		}}
	}
}

func setting(kv map[string]any) func(t *testing.T, w *eqWorld) any {
	return func(t *testing.T, w *eqWorld) any { return rpc.SetRequest{Patch: *eqPatch(t, kv)} }
}

func plain(v any) func(t *testing.T, w *eqWorld) any {
	return func(*testing.T, *eqWorld) any { return v }
}

func global(name, method string, req func(t *testing.T, w *eqWorld) any) eqCase {
	return eqCase{name: name, method: method, params: func(t *testing.T, w *eqWorld) json.RawMessage {
		raw, err := json.Marshal(req(t, w))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}}
}

func eqCases() []eqCase {
	perTarget := []func(string) eqCase{
		onTarget("attach", rpc.MethodAttach, plain(map[string]any{})),
		onTarget("form", rpc.MethodForm, plain(rpc.FormRequest{})),
		onTarget("lineage", rpc.MethodLineage, plain(map[string]any{})),
		onTarget("interrupt", rpc.MethodInterrupt, plain(rpc.InterruptRequest{})),
		onTarget("set mantra", rpc.MethodSet, setting(map[string]any{"mantra": "m"})),
		onTarget("set note", rpc.MethodSet, setting(map[string]any{"note": "n"})),
		onTarget("set system.scopes", rpc.MethodSet, setting(map[string]any{form.ScopesKey: []string{"read:*"}})),
		onTarget("set system.cwd", rpc.MethodSet, setting(map[string]any{"system.cwd": "/tmp"})),
		onTarget("set system.environment", rpc.MethodSet, setting(map[string]any{"system.environment.figaro_wire_dir": "/tmp/x"})),
		onTarget("set system.ttl", rpc.MethodSet, setting(map[string]any{"system.ttl": "1h"})),
		onTarget("set system.fanout", rpc.MethodSet, setting(map[string]any{"system.fanout.max_children": 9})),
		onTarget("set system.retired", rpc.MethodSet, setting(map[string]any{"system.retired": true})),
		onTarget("set system.reaped_until", rpc.MethodSet, setting(map[string]any{"system.reaped_until": 1})),
		onTarget("set runtime.forked_from", rpc.MethodSet, setting(map[string]any{"runtime.forked_from": "x"})),
		onTarget("set system.child_policy", rpc.MethodSet, setting(map[string]any{"system.child_policy.mantra": "kid"})),
		onTarget("set system.retention.mode", rpc.MethodSet, setting(map[string]any{"system.retention.mode": "none"})),
		onTarget("set target-aria", rpc.MethodSet, func(t *testing.T, w *eqWorld) any {
			return rpc.SetRequest{Patch: *eqPatch(t, map[string]any{"target-aria": w.outfit})}
		}),
		onTarget("study O on", rpc.MethodStudy, func(t *testing.T, w *eqWorld) any { return rpc.StudyRequest{FormID: w.outfit} }),
		onTarget("cast mantra on", rpc.MethodCast, func(t *testing.T, w *eqWorld) any {
			return rpc.CastRequest{RolePatch: eqPatch(t, map[string]any{"mantra": "seat"})}
		}),
		onTarget("decide on", rpc.MethodDecide, plain(rpc.DecideRequest{Question: "go?"})),
		onTarget("qua note on", rpc.MethodQua, func(t *testing.T, w *eqWorld) any {
			return rpc.QuaRequest{Text: "hi", Form: &rpc.FormInput{Patch: eqPatch(t, map[string]any{"note": "q"})}}
		}),
	}
	topTarget := []func(string) eqCase{
		func(target string) eqCase {
			return global("fork "+target, rpc.MethodFork, func(t *testing.T, w *eqWorld) any {
				return rpc.ForkRequest{FigaroID: w.id(target)}
			})
		},
		func(target string) eqCase {
			return global("fork "+target+" +mantra", rpc.MethodFork, func(t *testing.T, w *eqWorld) any {
				return rpc.ForkRequest{FigaroID: w.id(target), Patch: eqPatch(t, map[string]any{"mantra": "f"})}
			})
		},
		func(target string) eqCase {
			return global("fork "+target+" +scopes", rpc.MethodFork, func(t *testing.T, w *eqWorld) any {
				return rpc.ForkRequest{FigaroID: w.id(target), Patch: eqPatch(t, map[string]any{form.ScopesKey: []string{"*:*"}})}
			})
		},
		func(target string) eqCase {
			return global("kill "+target, rpc.MethodKill, func(t *testing.T, w *eqWorld) any {
				return rpc.KillRequest{FigaroID: w.id(target), Now: true}
			})
		},
	}
	var out []eqCase
	for _, target := range []string{"P", "C", "S", "O"} {
		for _, c := range perTarget {
			out = append(out, c(target))
		}
		for _, c := range topTarget {
			out = append(out, c(target))
		}
	}
	list := global("list", rpc.MethodList, plain(rpc.ListRequest{}))
	list.rows = true
	arias := eqCase{name: "form @arias", method: rpc.MethodForm, rows: true, params: func(t *testing.T, w *eqWorld) json.RawMessage {
		return withAria(t, rpc.AriasFormID, rpc.FormRequest{})
	}}
	formRowsOf := func(id string) eqCase {
		return eqCase{name: "form " + id, method: rpc.MethodForm, rows: true, params: func(t *testing.T, w *eqWorld) json.RawMessage {
			return withAria(t, id, rpc.FormRequest{})
		}}
	}
	return append(out,
		list,
		arias,
		formRowsOf(rpc.GlobalAriasFormID),
		formRowsOf(rpc.TopologyFormID),
		formRowsOf(rpc.RecentFormID),
		formRowsOf(rpc.TroupeFormID),
		global("create mock", rpc.MethodCreate, plain(rpc.CreateRequest{Outfits: []string{"mock"}})),
		global("create judged", rpc.MethodCreate, plain(rpc.CreateRequest{Outfits: []string{"judged"}})),
		global("create judged +mantra", rpc.MethodCreate, func(t *testing.T, w *eqWorld) any {
			return rpc.CreateRequest{Outfits: []string{"judged"}, Patch: eqPatch(t, map[string]any{"mantra": "c"})}
		}),
		global("create judged +scopes", rpc.MethodCreate, func(t *testing.T, w *eqWorld) any {
			return rpc.CreateRequest{Outfits: []string{"judged"}, Patch: eqPatch(t, map[string]any{form.ScopesKey: []string{"*:*"}})}
		}),
		global("create judged +environment", rpc.MethodCreate, func(t *testing.T, w *eqWorld) any {
			return rpc.CreateRequest{Outfits: []string{"judged"}, Patch: eqPatch(t, map[string]any{"system.environment.figaro_wire_dir": "/tmp/x"})}
		}),
		global("form.create +mantra", rpc.MethodFormCreate, func(t *testing.T, w *eqWorld) any {
			return rpc.FormCreateRequest{Patch: eqPatch(t, map[string]any{"mantra": "fc"})}
		}),
		global("form.create +target-aria", rpc.MethodFormCreate, func(t *testing.T, w *eqWorld) any {
			return rpc.FormCreateRequest{Patch: eqPatch(t, map[string]any{"target-aria": w.outfit})}
		}),
		global("decide default outfit", rpc.MethodDecide, plain(rpc.DecideRequest{Question: "go?"})),
		global("decide outfit mock", rpc.MethodDecide, plain(rpc.DecideRequest{Question: "go?", Outfit: "mock"})),
		global("decide choices", rpc.MethodDecide, plain(rpc.DecideRequest{Question: "which?", Outfit: "mock", Choices: []string{"a", "b"}})),
		global("decide provider override", rpc.MethodDecide, plain(rpc.DecideRequest{Question: "go?", Provider: "mock", Model: "m"})),
		global("status", rpc.MethodStatus, plain(map[string]any{})),
		global("gc.plan", rpc.MethodGCPlan, plain(rpc.GCPlanRequest{})),
		global("troupe.entries", rpc.MethodTroupeEntries, plain(rpc.TroupeEntriesRequest{Troupe: "t"})),
		global("import.status", rpc.MethodImportStatus, plain(rpc.ImportStatusRequest{Token: "x"})),
		global("node.learn", rpc.MethodNodeLearn, plain(rpc.NodeLearnRequest{Node: "n"})),
		global("worker.lease", rpc.MethodWorkerLease, plain(rpc.WorkerLeaseRequest{Pool: "p", Key: "k"})),
		global("worker.say", rpc.MethodWorkerSay, plain(rpc.WorkerSayRequest{Text: "s"})),
		global("outfits", rpc.MethodOutfits, plain(map[string]any{})),
	)
}

func (w *eqWorld) ask(t *testing.T, c eqCaller, k eqCase) string {
	t.Helper()
	params := k.params(t, w)
	if c.claim != "" {
		params = claimed(t, w.id(c.claim), params)
	}
	var out any
	var err error
	if c.viaBox {
		var raw json.RawMessage
		err = w.box.Call(context.Background(), k.method, params, &raw)
		if err == nil && k.rows {
			out, err = decodeRows(k.method, raw)
		}
	} else {
		fn, ok := w.h[k.method]
		if !ok {
			return "unserved"
		}
		out, err = fn(c.ctx(w), params)
	}
	if c.shadow {
		return w.decided(out, err, k) + fmt.Sprintf(" would=%d/%d/%d/%d", w.a.Policies.WouldRefused(authz.KeyGrants), w.a.Policies.WouldRefused(authz.ChildPolicy), w.a.Policies.WouldRefused(authz.LineageReach), w.a.Policies.WouldRefused(authz.Quota))
	}
	return w.decided(out, err, k)
}

func (w *eqWorld) decided(out any, err error, k eqCase) string {
	if err != nil {
		return "refused: " + w.normal(errText(err))
	}
	if !k.rows {
		return "ok"
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "unencodable: " + err.Error()
	}
	var shown []string
	for id, n := range w.names {
		if strings.Contains(string(raw), id[:12]) {
			shown = append(shown, n)
		}
	}
	slices.Sort(shown)
	return "ok rows=" + strings.Join(shown, ",")
}

func decodeRows(method string, raw json.RawMessage) (any, error) {
	if method == rpc.MethodList {
		var out rpc.ListResponse
		err := json.Unmarshal(raw, &out)
		return out, err
	}
	var out any
	err := json.Unmarshal(raw, &out)
	return out, err
}

func errText(err error) string {
	var rpcErr *jkrpc.Error
	if errors.As(err, &rpcErr) {
		return fmt.Sprintf("[%d] %s", rpcErr.Code, rpcErr.Message)
	}
	return err.Error()
}

var nonce = regexp.MustCompile(`[0-9a-f]{12,32}`)

func (w *eqWorld) normal(s string) string {
	for id, n := range w.names {
		s = strings.ReplaceAll(s, id, "<"+n+">")
		s = strings.ReplaceAll(s, id[:12], "<"+n+">")
	}
	s = strings.ReplaceAll(s, strconv.Itoa(os.Getuid()), "<uid>")
	s = nonce.ReplaceAllString(s, "<id>")
	return s
}

func TestDecisionEquivalence(t *testing.T) {
	golden := filepath.Join("testdata", "decision_equivalence.golden")
	callers, cases := eqCallers(), eqCases()
	got := make([]string, len(callers)*len(cases))
	t.Run("rows", func(t *testing.T) {
		for i, c := range callers {
			for j, k := range cases {
				row := &got[i*len(cases)+j]
				t.Run("", func(t *testing.T) {
					t.Parallel()
					w := newEqWorld(t, c.boxExtra)
					if c.shadow {
						w.a.Policies.SetEnforcement(authz.Enforcement{})
					}
					*row = fmt.Sprintf("%-26s | %-32s | %s\n", c.name, k.name, w.ask(t, c, k))
				})
			}
		}
	})
	var b strings.Builder
	for _, row := range got {
		b.WriteString(row)
	}
	if *updateDecisions {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v: run with -update-decisions to pin today's decisions", err)
	}
	if got := b.String(); got != string(want) {
		wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
		for i := 0; i < len(wl) || i < len(gl); i++ {
			var a, g string
			if i < len(wl) {
				a = wl[i]
			}
			if i < len(gl) {
				g = gl[i]
			}
			if a != g {
				t.Errorf("row %d changed:\n  was: %s\n  now: %s", i+1, a, g)
			}
		}
	}
}
