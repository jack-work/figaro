package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func mkSnap(t *testing.T, kv map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %q: %v", k, err)
		}
		out[k] = b
	}
	return out
}

func TestExpandAtRefs_BasicSubstitution(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"cwd":   "/home/gluck/dev",
		"model": "sonnet-9.5",
	})
	cases := map[string]string{
		"summarize @cwd":                            "summarize /home/gluck/dev",
		"@model running in @cwd":                    "sonnet-9.5 running in /home/gluck/dev",
		"start @model":                              "start sonnet-9.5",
		"@cwd":                                      "/home/gluck/dev",
		"plain text":                                "plain text",
		"":                                          "",
		"trailing @model.":                          "trailing sonnet-9.5.",
		"comma @model,end":                          "comma sonnet-9.5,end",
		"newline\n@model\nafter":                    "newline\nsonnet-9.5\nafter",
	}
	for in, want := range cases {
		got := expandAtRefs(in, snap)
		if got != want {
			t.Errorf("expandAtRefs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandAtRefs_PermissiveOnMissing(t *testing.T) {
	snap := mkSnap(t, map[string]any{"cwd": "/x"})
	cases := []string{
		"@missing key",
		"unknown @nope here",
		"@",      // bare @
		"@.foo",  // invalid key shape
		"@_okay", // valid shape, but missing -> literal
	}
	for _, in := range cases {
		got := expandAtRefs(in, snap)
		if got != in {
			t.Errorf("expandAtRefs(%q) = %q, want literal passthrough", in, got)
		}
	}
}

func TestExpandAtRefs_WordBoundary_PreservesEmailAndPaths(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"example": "EXPANDED",
		"com":     "COM",
	})
	// @ that is preceded by an alphanumeric or underscore character
	// (i.e. inside a word like an email address) must NOT expand.
	cases := map[string]string{
		"me@example.com":   "me@example.com",
		"foo@example":      "foo@example",
		// Punctuation boundaries DO permit expansion: someone writing
		// `path/@example` or `=@count` is opting in.
		"path/@example":    "path/EXPANDED",
		"(@example)":       "(EXPANDED)",
		// And a leading @, or @ after whitespace, expands as expected.
		"@example here":    "EXPANDED here",
		"hi @example":      "hi EXPANDED",
		"\t@example":       "\tEXPANDED",
	}
	for in, want := range cases {
		got := expandAtRefs(in, snap)
		if got != want {
			t.Errorf("expandAtRefs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandAtRefs_DottedKey(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"system.environment.path": "/usr/bin",
		"system":                  "WHOLE",
	})
	// Longest valid prefix wins via greedy match; "system.environment.path"
	// is preferred over "system" when the longer key is in the snapshot.
	got := expandAtRefs("env @system.environment.path end", snap)
	want := "env /usr/bin end"
	if got != want {
		t.Errorf("expandAtRefs(dotted) = %q, want %q", got, want)
	}

	// Trailing dot is not part of the key (it's sentence punctuation).
	got = expandAtRefs("end with @system.", snap)
	want = "end with WHOLE."
	if got != want {
		t.Errorf("expandAtRefs(trailing dot) = %q, want %q", got, want)
	}
}

func TestExpandAtRefs_NonStringValues(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"count": 42,
		"flag":  true,
		"list":  []int{1, 2, 3},
	})
	cases := map[string]string{
		"n=@count":  "n=42",
		"f=@flag":   "f=true",
		"l=@list":   "l=[1,2,3]",
	}
	for in, want := range cases {
		got := expandAtRefs(in, snap)
		if got != want {
			t.Errorf("expandAtRefs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandAtRefs_EmptySnapshotNoop(t *testing.T) {
	got := expandAtRefs("hi @cwd there", nil)
	if got != "hi @cwd there" {
		t.Errorf("nil snapshot must passthrough, got %q", got)
	}
	got = expandAtRefs("hi @cwd there", map[string]json.RawMessage{})
	if got != "hi @cwd there" {
		t.Errorf("empty snapshot must passthrough, got %q", got)
	}
}

func TestExpandAtRefs_NoAtMeansNoAlloc(t *testing.T) {
	snap := mkSnap(t, map[string]any{"k": "v"})
	in := strings.Repeat("plain text without any sigil. ", 100)
	got := expandAtRefs(in, snap)
	if got != in {
		t.Errorf("plain text mutated unexpectedly")
	}
}

func TestReadAtKey(t *testing.T) {
	cases := []struct {
		in      string
		wantKey string
		wantN   int
	}{
		{"cwd", "cwd", 3},
		{"cwd ", "cwd", 3},
		{"cwd.foo", "cwd.foo", 7},
		{"cwd.", "cwd", 3},
		{"_foo", "_foo", 4},
		{"123", "", 0},
		{".foo", "", 0},
		{"", "", 0},
		{"cwd!extra", "cwd", 3},
	}
	for _, c := range cases {
		k, n := readAtKey(c.in)
		if k != c.wantKey || n != c.wantN {
			t.Errorf("readAtKey(%q) = (%q,%d), want (%q,%d)", c.in, k, n, c.wantKey, c.wantN)
		}
	}
}
