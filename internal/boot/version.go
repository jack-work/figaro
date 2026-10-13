package boot

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// Set with -ldflags -X; strings because -X cannot set a bool.
var (
	commit      = ""
	commitTime  = ""
	commitDirty = ""
	semver      = ""
)

// BuildRevision is empty when unknown; callers must skip the comparison rather
// than treat it as a mismatch.
func BuildRevision() string {
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

func moduleVersion(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return ""
}

func PrintVersion(w io.Writer) {
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

func BuildIdentityKind(s string) string {
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
