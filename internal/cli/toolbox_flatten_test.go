package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// AN EDIT MUST SHOW THE EDIT.
//
// `edits` is a list of objects: the one argument shape in figaro that is not
// a scalar, and it used to render through fmt's %v:
//
//	edits [map[new_text:// render draws one block… old_text:…]]
//
// Go syntax, unspecified map order, and both strings crushed onto one line
// with their newlines gone. Named by path instead, each leaf arrives as its
// own value and the block draws it the way it draws any other string.
func TestNestedArgsFlattenByPath(t *testing.T) {
	n := livedoc.Node{
		Type: livedoc.NodeTool, Name: "edit",
		Args: map[string]any{
			"path": "/var/tmp/probe/compose.go",
			"edits": []any{
				map[string]any{"old_text": "func a() {\n\treturn\n}", "new_text": "func a() error {\n\treturn nil\n}"},
				map[string]any{"old_text": "x", "new_text": "y"},
			},
		},
	}
	fields := toolArgFields(n)

	got := map[string]string{}
	var names []string
	for _, f := range fields {
		got[f.Name] = f.Value
		names = append(names, f.Name)
	}
	for _, want := range []string{"edits[0].new_text", "edits[0].old_text", "edits[1].new_text", "path"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing field %q; got %v", want, names)
		}
	}
	if v := got["edits[0].new_text"]; v != "func a() error {\n\treturn nil\n}" {
		t.Errorf("the edit's text must arrive verbatim, newlines and tabs intact: %q", v)
	}
	for _, f := range fields {
		if strings.Contains(f.Value, "map[") {
			t.Errorf("Go syntax leaked into %s: %q", f.Name, f.Value)
		}
	}
	// Sorted, so the block does not reshuffle: edits[…] before path.
	if names[0] != "edits[0].new_text" || names[len(names)-1] != "path" {
		t.Errorf("fields are not in sorted order: %v", names)
	}
}

// Scalars that are not strings are JSON, which is the notation they arrived
// in: never %v, and never quoted like a string.
func TestNonStringScalarsAreJSON(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "bash",
		Args: map[string]any{"command": "true", "timeout": float64(240), "quiet": true}}
	got := map[string]string{}
	for _, f := range toolArgFields(n) {
		got[f.Name] = f.Value
	}
	if got["timeout"] != "240" || got["quiet"] != "true" {
		t.Errorf("scalars = %v", got)
	}
	if got["command"] != "true" {
		t.Errorf("a string must not be re-quoted: %q", got["command"])
	}
}

// The streaming phase is untouched: while arguments arrive there is a raw
// prefix and it is parsed as it stands.
func TestStreamingPrefixStillParsed(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "edit", Input: `{"path": "x.go", "edits": [{"old_te`}
	fields := toolArgFields(n)
	if len(fields) == 0 || fields[len(fields)-1].Name != "path" {
		t.Fatalf("streaming fields = %+v", fields)
	}
}
