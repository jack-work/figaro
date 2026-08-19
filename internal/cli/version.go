package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// commit and commitTime are populated via -ldflags at build time:
var (
	commit     = ""
	commitTime = ""
	// commitDirty is "true" or "": string so it survives -ldflags -X.
	commitDirty = ""
	// semver is the release version, injected from flake.nix's `version`.
	// It is the single live version: flake.nix declares it, the release
	// script mints the matching vX.Y.Z tag from it, and this is where it
	// becomes visible on a running binary. Empty for a bare `go build`,
	// where the revision is the only identity there is.
	semver = ""
)

// runVersion prints binary identity: VCS revision, build state,
// Go version, OS/arch, and the path under which this binary was built.
// Designed so a wrong binary on $PATH stands out.
func runVersion() {
	printVersion(os.Stdout)
}

// buildRevision is this binary's VCS revision, from -ldflags when set and
// debug.ReadBuildInfo otherwise. Empty when neither knows (a bare `go build`
// outside a repo), in which case callers must skip build comparisons rather
// than treat unknown as mismatched.
func buildRevision() string {
	if commit != "" {
		return commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return moduleVersion(info)
}

// moduleVersion is the release Go stamped into a `<module>@version` install.
// Empty for anything built from a local tree, where Go writes "(devel)".
func moduleVersion(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return ""
}

func printVersion(w io.Writer) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(w, "figaro (build info unavailable)")
		return
	}

	rev := commit
	modified := commitDirty == "true"
	buildTime := commitTime
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if rev == "" {
				rev = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				modified = true
			}
		case "vcs.time":
			if buildTime == "" {
				buildTime = s.Value
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	dirty := ""
	if modified {
		dirty = "-dirty"
	}

	module := info.Main.Path
	if module == "" {
		module = "(unknown)"
	}
	release := semver
	if release == "" {
		release = moduleVersion(info)
	}
	switch {
	case release != "" && rev != "":
		fmt.Fprintf(w, "figaro %s (%s%s, %s/%s, %s)\n", release, rev, dirty, runtime.GOOS, runtime.GOARCH, info.GoVersion)
	case release != "":
		fmt.Fprintf(w, "figaro %s (%s/%s, %s)\n", release, runtime.GOOS, runtime.GOARCH, info.GoVersion)
	case rev != "":
		fmt.Fprintf(w, "figaro %s%s (%s/%s, %s)\n", rev, dirty, runtime.GOOS, runtime.GOARCH, info.GoVersion)
	default:
		fmt.Fprintf(w, "figaro unknown (%s/%s, %s)\n", runtime.GOOS, runtime.GOARCH, info.GoVersion)
	}
	fmt.Fprintf(w, "  module:    %s\n", module)
	fmt.Fprintf(w, "  exe:       %s\n", currentExe())
	if buildTime != "" {
		fmt.Fprintf(w, "  vcs.time:  %s\n", buildTime)
	}
}

func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "?"
	}
	return exe
}

// buildIdentityKind names WHICH identity scheme a build string belongs to.
func buildIdentityKind(s string) string {
	if s == "" {
		return "unknown"
	}
	if strings.HasPrefix(s, "v") && strings.ContainsRune(s, '.') {
		return "module"
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "other"
		}
	}
	return "revision"
}
