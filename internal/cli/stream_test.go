package cli

import "testing"

func TestExtractPartialDetail(t *testing.T) {
	cases := []struct {
		name, tool, partial, want string
	}{
		{"empty", "bash", "", ""},
		{"no key yet", "bash", `{"com`, ""},
		{"key started", "bash", `{"command": "`, ""},
		{"partial value", "bash", `{"command": "figaro --help 2>&1 | hea`, "figaro --help 2>&1 | hea"},
		{"complete value", "bash", `{"command": "pwd && date"}`, "pwd && date"},
		{"escaped quote", "bash", `{"command": "echo \"hi\""}`, `echo "hi"`},
		{"path partial", "read", `{"path": "/home/gluck/dev/fig`, "/home/gluck/dev/fig"},
		{"path complete", "edit", `{"path": "/tmp/foo.go", "edits": []}`, "/tmp/foo.go"},
		{"unknown tool", "unknown", `{"x": "y"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPartialDetail(tc.tool, tc.partial)
			if got != tc.want {
				t.Errorf("extractPartialDetail(%q, %q) = %q, want %q", tc.tool, tc.partial, got, tc.want)
			}
		})
	}
}
