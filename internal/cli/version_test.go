package cli

import (
	"runtime/debug"
	"testing"
)

func TestModuleVersion(t *testing.T) {
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"nil", nil, ""},
		{"local tree", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, ""},
		{"unset", &debug.BuildInfo{Main: debug.Module{Version: ""}}, ""},
		{"release", &debug.BuildInfo{Main: debug.Module{Version: "v0.17.0"}}, "v0.17.0"},
		{
			"pseudo",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.3.4-0.20260717073758-202337881eeb"}},
			"v0.3.4-0.20260717073758-202337881eeb",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := moduleVersion(c.info); got != c.want {
				t.Fatalf("moduleVersion = %q, want %q", got, c.want)
			}
		})
	}
}
