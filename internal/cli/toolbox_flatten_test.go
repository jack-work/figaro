package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// A list-of-objects argument renders by path, each leaf as its own value, never
// through %v.
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
	// Sorted, so the block does not reshuffle.
	if names[0] != "edits[0].new_text" || names[len(names)-1] != "path" {
		t.Errorf("fields are not in sorted order: %v", names)
	}
}

// Non-string scalars are JSON, never %v, never quoted.
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

// While arguments stream there is a raw prefix, parsed as it stands.
func TestStreamingPrefixStillParsed(t *testing.T) {
	n := livedoc.Node{Type: livedoc.NodeTool, Name: "edit", Input: `{"path": "x.go", "edits": [{"old_te`}
	fields := toolArgFields(n)
	if len(fields) == 0 || fields[len(fields)-1].Name != "path" {
		t.Fatalf("streaming fields = %+v", fields)
	}
}
