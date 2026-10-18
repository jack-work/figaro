package config

import "testing"

func TestWorkspaceRootIsAbsoluteOrUnset(t *testing.T) {
	for _, c := range []struct {
		root, want string
		ok         bool
	}{
		{"", "", true},
		{"/var/lib/figaro/workspaces/", "/var/lib/figaro/workspaces", true},
		{"workspaces", "", false},
	} {
		l := &Loaded{Config: Config{Sandbox: SandboxConfig{WorkspaceRoot: c.root}}}
		got, err := l.WorkspaceRoot()
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%q: %q, %v", c.root, got, err)
		}
	}
}
