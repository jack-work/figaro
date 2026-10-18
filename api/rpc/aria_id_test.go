package rpc

import "testing"

func TestValidateAriaID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"empty", "", false},
		{"single letter", "a", true},
		{"uuid prefix", "060bab89", true},
		{"hyphenated", "my-aria", true},
		{"underscore", "my_aria", true},
		{"alnum mixed", "abc123_X-Y", true},
		{"max length", string(make([]byte, 0, 64)) + repeat("a", 64), true},
		{"too long", repeat("a", 65), false},
		{"slash", "foo/bar", false},
		{"dot", "foo.bar", false},
		{"dotdot", "..", false},
		{"space", "foo bar", false},
		{"dollar", "foo$bar", false},
		{"unicode", "fig\u00e9ro", false},
		{"null byte", "foo\x00bar", false},
		{"leading hyphen", "-foo", true},
		{"newline", "foo\nbar", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAriaID(tc.id)
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected error, got nil for id %q", tc.id)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// A client may qualify an id with its node; storage never sees one.
func TestValidateAriaRef(t *testing.T) {
	for ref, ok := range map[string]bool{
		"abcd1234": true,
		"0e6f7dbf-0000-4000-8000-000000000000/abcd1234": true,
		"node/@form1234": true,
		"/abcd":          true,
		"node/":          false,
		"a/b/c":          false,
		"../x":           false,
	} {
		if err := ValidateAriaRef(ref); (err == nil) != ok {
			t.Errorf("ValidateAriaRef(%q) = %v, want ok=%v", ref, err, ok)
		}
	}
	if err := ValidateAriaID("node/abcd"); err == nil {
		t.Error("a bare id must not carry a node")
	}
}
