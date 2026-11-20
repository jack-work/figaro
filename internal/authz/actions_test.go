package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/jkauthz"
)

func declaredMethods(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "api", "rpc", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !strings.HasPrefix(id.Name, "Method") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, _ := strconv.Unquote(lit.Value)
					out[id.Name] = v
				}
			}
		}
	}
	if len(out) < 40 {
		t.Fatalf("found only %d method constants; the scan is broken", len(out))
	}
	return out
}

// A method without a verb would be served without a policy decision, or
// refused for a reason nobody wrote down. Declaring one is the moment to
// decide which.
func TestEveryMethodHasAVerb(t *testing.T) {
	for name, method := range declaredMethods(t) {
		if _, ok := VerbOf(method); !ok {
			t.Errorf("%s (%q) has no action in the authz table", name, method)
		}
	}
}

func TestTheVerbTableNamesOnlyDeclaredMethods(t *testing.T) {
	declared := map[string]bool{}
	for _, m := range declaredMethods(t) {
		declared[m] = true
	}
	for m := range actions {
		if !declared[m] {
			t.Errorf("the authz table declares %q, which no Method constant declares", m)
		}
	}
}

// hello is how a caller learns its own principal, so it is the one method a
// policy never sees. Anything else exempt would be a hole.
func TestOnlyHelloIsExempt(t *testing.T) {
	for m, a := range actions {
		v := a.Verb
		if v == rpc.VerbNone && m != rpc.MethodHello {
			t.Errorf("%s is exempt from policy", m)
		}
	}
}

// use is granted on sandboxes, not on methods: it is checked where a form
// names a sandbox, whichever method carried it.
func TestUseIsNeverAMethodVerb(t *testing.T) {
	for m, a := range actions {
		v := a.Verb
		if v == rpc.VerbUse {
			t.Errorf("%s is classified as use", m)
		}
	}
}

// join is granted on troupe:<name>, a resource the guard does not know, so
// only the troupe handlers that check it themselves may carry it.
func TestJoinIsCarriedOnlyByTheTroupeHandlers(t *testing.T) {
	for m, a := range actions {
		v := a.Verb
		if v == rpc.VerbJoin && m != rpc.MethodTroupeSign && m != rpc.MethodTroupeIdentity {
			t.Errorf("%s is classified as join", m)
		}
	}
}

func TestEveryClassifiedVerbIsAPolicyVerb(t *testing.T) {
	known := map[rpc.Verb]bool{rpc.VerbNone: true}
	for _, v := range rpc.Verbs() {
		known[v] = true
	}
	for m, a := range actions {
		v := a.Verb
		if !known[v] {
			t.Errorf("%s has verb %q, which rpc.Verbs() does not list", m, v)
		}
	}
}

// decide is granted on outfit:<name> as well as on arias, so only
// figaro.decide carries it; send stays sufficient for the callers that
// decided under send before the verb existed.
func TestDecideIsCarriedOnlyByDecideAndSendStillSuffices(t *testing.T) {
	for m, a := range actions {
		v := a.Verb
		if v == rpc.VerbDecide && m != rpc.MethodDecide {
			t.Errorf("%s is classified as decide", m)
		}
	}
	verbs, _ := VerbsOf(rpc.MethodDecide)
	if len(verbs) != 2 || verbs[0] != rpc.VerbDecide || verbs[1] != rpc.VerbSend {
		t.Fatalf("figaro.decide is checked as %v, want [decide send]", verbs)
	}
}

// The table is the only way through the guard: a method it does not declare
// is refused for every caller on every door, the owner on the trusted local
// door included.
func TestAnUndeclaredMethodIsRefusedOnEveryDoor(t *testing.T) {
	owner := jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}
	p := NewPolicies(owner, nil)
	if err := p.Reload(""); err != nil {
		t.Fatal(err)
	}
	for _, d := range []authn.Door{authn.DoorUnknown, authn.DoorInProcess, authn.DoorLocal, authn.DoorNode, authn.DoorWeb, authn.DoorBox, authn.DoorHost} {
		got := p.Call(Request{Subject: jkauthz.Subject{Principal: owner}, Door: d, Method: "figaro.undeclared"})
		if got.Allow || !strings.Contains(got.Reason, "figaro.undeclared has no verb") {
			t.Errorf("door %s: %+v", d, got)
		}
	}
	if got := p.Call(Request{Subject: jkauthz.Subject{Principal: owner}, Door: authn.DoorLocal, Method: rpc.MethodStatus}); !got.Allow {
		t.Fatalf("canary: the owner is refused a declared method: %+v", got)
	}
}
