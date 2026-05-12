package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

// commit and commitTime are populated via -ldflags at build time:
//
//	go install -ldflags="-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" ./cmd/figaro
//
// debug.ReadBuildInfo provides the same data automatically for normal
// repos; the ldflags path is the fallback for git worktrees and bare-repo
// layouts where Go's VCS auto-detection comes up empty.
var (
	commit     = ""
	commitTime = ""
	// commitDirty is "true" or "" — string so it survives -ldflags -X.
	commitDirty = ""
)

// runVersion prints binary identity: VCS revision, build state,
// Go version, OS/arch, and the path under which this binary was built.
// Designed so a wrong binary on $PATH stands out.
func runVersion() {
	printVersion(os.Stdout)
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
	if rev == "" {
		rev = "unknown"
	} else if len(rev) > 12 {
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
	fmt.Fprintf(w, "figaro %s%s (%s/%s, %s)\n", rev, dirty, runtime.GOOS, runtime.GOARCH, info.GoVersion)
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
