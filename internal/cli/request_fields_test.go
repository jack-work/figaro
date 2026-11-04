package cli

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

const additiveFieldsFile = "testdata/additive_request_fields.txt"

func rpcRequestFields(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "api", "rpc")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(ts.Name.Name, "Request") {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, fl := range st.Fields.List {
					for _, name := range fl.Names {
						if name.IsExported() {
							out = append(out, ts.Name.Name+"."+name.Name)
						}
					}
					if len(fl.Names) == 0 {
						out = append(out, ts.Name.Name+"."+embeddedName(fl.Type))
					}
				}
				return true
			})
		}
	}
	slices.Sort(out)
	return out
}

func embeddedName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return embeddedName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return "?"
}

func additiveFields(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(additiveFieldsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	return out
}

// Every field a CLI can put in a request is either gated by a capability, so
// a daemon that does not know it refuses by name, or recorded as safe for an
// older daemon to drop. A new field with neither fails here, before an old
// daemon can drop it in silence.
func TestEveryRequestFieldIsGatedOrMarkedAdditive(t *testing.T) {
	additive := additiveFields(t)
	var missing []string
	for _, f := range rpcRequestFields(t) {
		if _, gated := requestFieldCapabilities[f]; gated || additive[f] {
			continue
		}
		missing = append(missing, f)
	}
	if len(missing) > 0 {
		t.Fatalf("request fields with no capability and no additive mark:\n  %s\n"+
			"map each to a capability in requestFieldCapabilities (an older daemon would get it wrong in silence),\n"+
			"or list it in %s (an older daemon may drop it safely)", strings.Join(missing, "\n  "), additiveFieldsFile)
	}
}

func TestTheCapabilityTableNamesRealFields(t *testing.T) {
	fields := rpcRequestFields(t)
	additive := additiveFields(t)
	for f := range requestFieldCapabilities {
		if !slices.Contains(fields, f) {
			t.Errorf("requestFieldCapabilities names %s, which is not a field of an api/rpc request", f)
		}
		if additive[f] {
			t.Errorf("%s is both gated and marked additive", f)
		}
	}
}

func TestAGatedFieldIsRefusedWhenItIsSetAndTheDaemonLacksIt(t *testing.T) {
	resetDaemonSeen(t)
	sawDaemon("the daemon", daemonIdentity{build: "b", release: "0.52.5", protocol: 1}, "a")
	if err := refuseUnknownFields(rpc.QuaRequest{Text: "hi"}); err != nil {
		t.Fatalf("a request without the gated field must pass: %v", err)
	}
	err := refuseUnknownFields(rpc.QuaRequest{Text: "hi", Images: []rpc.QuaImage{{MimeType: "image/png", Data: "AA"}}})
	if err == nil || !strings.Contains(err.Error(), string(rpc.CapQuaImages)) {
		t.Fatalf("images to a daemon without %s: %v", rpc.CapQuaImages, err)
	}
}
