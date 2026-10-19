package rpc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func declaredMethods(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
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
			t.Errorf("%s (%q) has no verb in methodVerbs", name, method)
		}
	}
}

func TestTheVerbTableNamesOnlyDeclaredMethods(t *testing.T) {
	declared := map[string]bool{}
	for _, m := range declaredMethods(t) {
		declared[m] = true
	}
	for m := range methodVerbs {
		if !declared[m] {
			t.Errorf("methodVerbs classifies %q, which no Method constant declares", m)
		}
	}
}

// hello is how a caller learns its own principal, so it is the one method a
// policy never sees. Anything else exempt would be a hole.
func TestOnlyHelloIsExempt(t *testing.T) {
	for m, v := range methodVerbs {
		if v == VerbNone && m != MethodHello {
			t.Errorf("%s is exempt from policy", m)
		}
	}
}

// use is granted on sandboxes, not on methods: it is checked where a form
// names a sandbox, whichever method carried it.
func TestUseIsNeverAMethodVerb(t *testing.T) {
	for m, v := range methodVerbs {
		if v == VerbUse {
			t.Errorf("%s is classified as use", m)
		}
	}
}

// join is granted on troupe:<name>, a resource the guard does not know, so
// only the troupe handlers that check it themselves may carry it.
func TestJoinIsCarriedOnlyByTheTroupeHandlers(t *testing.T) {
	for m, v := range methodVerbs {
		if v == VerbJoin && m != MethodTroupeSign && m != MethodTroupeIdentity {
			t.Errorf("%s is classified as join", m)
		}
	}
}

func TestEveryClassifiedVerbIsAPolicyVerb(t *testing.T) {
	known := map[Verb]bool{VerbNone: true}
	for _, v := range Verbs() {
		known[v] = true
	}
	for m, v := range methodVerbs {
		if !known[v] {
			t.Errorf("%s has verb %q, which Verbs() does not list", m, v)
		}
	}
}
