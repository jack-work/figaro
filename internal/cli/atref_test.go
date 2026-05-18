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
		"summarize @cwd!":         "summarize /home/gluck/dev",
		"@model! running in @cwd!": "sonnet-9.5 running in /home/gluck/dev",
		"start @model!":           "start sonnet-9.5",
		"@cwd!":                   "/home/gluck/dev",
		"plain text":              "plain text",
		"":                        "",
		"newline\n@model!\nafter": "newline\nsonnet-9.5\nafter",
	}
	for in, want := range cases {
		got := expandAtRefs(in, snap)
		if got != want {
			t.Errorf("expandAtRefs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandAtRefs_RequiresBangTerminator(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"cwd":   "/x",
		"model": "m",
	})
	// Without a trailing "!" no expansion happens — the @ is literal.
	// This is the whole point of the explicit terminator: zero false
	// positives. Email addresses, code snippets, twitter handles all
	// pass through untouched.
	cases := []string{
		"summarize @cwd",          // no !
		"summarize @cwd then",     // ! never appears
		"me@example.com",          // typical email
		"foo@cwd@model",           // chained @s with no terminator
		"start @model.",           // sentence punctuation, no !
		"@cwd,end",                // comma, no !
		"@cwd ",                   // whitespace, no !
		"hi @cwd@@",               // garbage tail, no !
	}
	for _, in := range cases {
		got := expandAtRefs(in, snap)
		if got != in {
			t.Errorf("expandAtRefs(%q) expanded to %q; want literal", in, got)
		}
	}
}

func TestExpandAtRefs_PermissiveOnMissing(t *testing.T) {
	snap := mkSnap(t, map[string]any{"cwd": "/x"})
	// Even with a "!" terminator, unknown keys are left literal so
	// typos surface in the prompt the model sees.
	cases := []string{
		"start @missing! key",
		"@nope!",
		"@!",      // empty key with terminator
		"@.foo!",  // invalid key shape, terminator present
	}
	for _, in := range cases {
		got := expandAtRefs(in, snap)
		if got != in {
			t.Errorf("expandAtRefs(%q) = %q, want literal passthrough", in, got)
		}
	}
}

func TestExpandAtRefs_EmailsAndCodeStayLiteral(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"example": "EXPANDED",
		"com":     "COM",
		"cwd":     "/x",
	})
	cases := map[string]string{
		// Emails: no terminator, no expansion. This is the headline case.
		"me@example.com":      "me@example.com",
		"foo@example":         "foo@example",
		"a@b.c":               "a@b.c",
		// Code-ish: still no terminator, still literal.
		"path/@example":       "path/@example",
		"(@example)":          "(@example)",
		"\"@example\"":        "\"@example\"",
		// But with explicit "!", expansion fires regardless of context.
		"me@example.com is @cwd!": "me@example.com is /x",
		"(@example!)":            "(EXPANDED)",
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
	got := expandAtRefs("env @system.environment.path! end", snap)
	want := "env /usr/bin end"
	if got != want {
		t.Errorf("expandAtRefs(dotted) = %q, want %q", got, want)
	}

	// A trailing dot is not part of the key; without a "!" right
	// after the key, no expansion.
	got = expandAtRefs("end with @system.", snap)
	want = "end with @system."
	if got != want {
		t.Errorf("expandAtRefs(trailing dot no bang) = %q, want %q", got, want)
	}

	// "@system!." — terminator immediately after the key, then a dot
	// of sentence punctuation. Expands.
	got = expandAtRefs("end with @system!.", snap)
	want = "end with WHOLE."
	if got != want {
		t.Errorf("expandAtRefs(bang then dot) = %q, want %q", got, want)
	}
}

func TestExpandAtRefs_NonStringValues(t *testing.T) {
	snap := mkSnap(t, map[string]any{
		"count": 42,
		"flag":  true,
		"list":  []int{1, 2, 3},
	})
	cases := map[string]string{
		"n=@count!": "n=42",
		"f=@flag!":  "f=true",
		"l=@list!":  "l=[1,2,3]",
	}
	for in, want := range cases {
		got := expandAtRefs(in, snap)
		if got != want {
			t.Errorf("expandAtRefs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandAtRefs_EmptySnapshotNoop(t *testing.T) {
	got := expandAtRefs("hi @cwd! there", nil)
	if got != "hi @cwd! there" {
		t.Errorf("nil snapshot must passthrough, got %q", got)
	}
	got = expandAtRefs("hi @cwd! there", map[string]json.RawMessage{})
	if got != "hi @cwd! there" {
		t.Errorf("empty snapshot must passthrough, got %q", got)
	}
}

func TestExpandAtRefs_NoAtMeansPassthrough(t *testing.T) {
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
