package rpc_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestContractImportsNothingInternal is the law this tree exists to keep.
//
// api/ is the wire contract: the types a client decodes and the method names
// it calls. A client is a CLI today, a browser or an SDK tomorrow, and NONE of
// them can compile a package that reaches into internal/ -- Go forbids the
// import outright across modules, so a single leak makes the whole contract
// unusable outside this repo without anyone noticing at build time.
//
// The May 2026 tightening (plans/rpc-surface-tightening.md) left a SHAPE and
// no rule, and the surface drifted for three months. This is the rule. It
// costs one directory walk and it fails the first time someone reaches for a
// server type instead of declaring a wire one.
func TestContractImportsNothingInternal(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.Contains(p, "/internal/") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("api/%s imports %s: the contract must be buildable by a client, "+
					"and no client can import internal/. Declare the wire type here instead.", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("walked no files: the guard is checking nothing")
	}
}
