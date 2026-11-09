package boot

import (
	"runtime/debug"
	"strings"
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

// figscope compares pager latency by service.version. A dev build that called
// itself 0.58.3 counted as the release, and an unstamped build said
// "unknown" with no way to tell builds apart.
func TestServiceVersionNeverPassesADevBuildOffAsARelease(t *testing.T) {
	saved := [4]string{semver, commit, commitDirty, build}
	t.Cleanup(func() { semver, commit, commitDirty, build = saved[0], saved[1], saved[2], saved[3] })
	for _, c := range []struct {
		semver, commit, dirty, build, want string
	}{
		{"0.58.3", "66a8d052aaaa", "", "", "0.58.3"},
		{"0.58.3", "66a8d052aaaa", "true", "", "0.58.3+dirty"},
		{"0.58.3", "66a8d052aaaa", "", "dev", "0.58.3+dev"},
		{"", "66a8d052aaaa", "", "", "dev-66a8d052aaaa"},
	} {
		semver, commit, commitDirty, build = c.semver, c.commit, c.dirty, c.build
		if got := ServiceVersion(); got != c.want {
			t.Errorf("semver %q commit %q dirty %q build %q: %q, want %q", c.semver, c.commit, c.dirty, c.build, got, c.want)
		}
	}
	semver, commit, commitDirty, build = "", "", "", ""
	if got := ServiceVersion(); got != "dev" && !strings.HasPrefix(got, "dev-") {
		t.Errorf("an unstamped build reported %q", got)
	}
}
