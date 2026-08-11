package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE TOOL'S ARGUMENTS ARE SCALARS, ALL THE WAY DOWN.
//
// Not taste: under Claude's tool-call format a scalar is handed over verbatim
// and the server encodes it, while a list or object is JSON the model must
// author and escape by hand. This tool's array-of-objects was the only place
// figaro ever asked for that, and the only place malformed arguments were ever
// measured. A schema that grows one back would bring the failure with it.
func TestEditParametersAreAllScalarStrings(t *testing.T) {
	params, ok := (&EditTool{}).Parameters().(map[string]interface{})
	if !ok {
		t.Fatal("parameters must be an object schema")
	}
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("no properties")
	}
	for name, raw := range props {
		p, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("%s: not a schema", name)
		}
		if p["type"] != "string" {
			t.Errorf("%s has type %v: every argument must be a scalar string, "+
				"or the model has to hand-author JSON for it", name, p["type"])
		}
		if _, nested := p["items"]; nested {
			t.Errorf("%s carries `items`: an array is back in the schema", name)
		}
	}
	for _, want := range []string{"path", "old_text", "new_text"} {
		if _, ok := props[want]; !ok {
			t.Errorf("missing %q", want)
		}
	}
	if _, gone := props["edits"]; gone {
		t.Error("`edits` is still advertised; the nested shape must not be offered")
	}
}

func TestEditAppliesOneScalarReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("func a() {\n\treturn\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(dir)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":     "x.go",
		"old_text": "func a() {\n\treturn\n}",
		"new_text": "func a() error {\n\treturn nil\n}",
	}, nil); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "return nil") {
		t.Errorf("file = %q", got)
	}
}

// A model reads its own transcript as an example. Histories are full of the
// old shape, so it is still accepted: just never advertised.
func TestLegacyNestedShapeStillApplies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEditTool(dir).Execute(context.Background(), map[string]interface{}{
		"path": "x.txt",
		"edits": []interface{}{
			map[string]interface{}{"old_text": "alpha", "new_text": "ALPHA"},
		},
	}, nil); err != nil {
		t.Fatalf("legacy shape must still apply: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "ALPHA") {
		t.Errorf("file = %q", got)
	}
}

func TestEditWithoutOldTextSaysSo(t *testing.T) {
	_, err := parseEditArgs(map[string]interface{}{"path": "x.txt"})
	if err == nil || !strings.Contains(err.Error(), "old_text") {
		t.Errorf("err = %v, want it to name old_text", err)
	}
}
