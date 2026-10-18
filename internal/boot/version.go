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

type VersionInfo struct {
	Release  string `json:"release,omitempty"`
	Revision string `json:"revision,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Go       string `json:"go"`
	Module   string `json:"module"`
	Exe      string `json:"exe"`
	VCSTime  string `json:"vcs_time,omitempty"`
	Node     string `json:"node,omitempty"`
}

func Version() (VersionInfo, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return VersionInfo{}, false
	}
	v := VersionInfo{
		Revision: commit,
		Dirty:    commitDirty == "true",
		VCSTime:  commitTime,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Go:       info.GoVersion,
		Module:   info.Main.Path,
		Exe:      currentExe(),
		Release:  semver,
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if v.Revision == "" {
				v.Revision = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				v.Dirty = true
			}
		case "vcs.time":
			if v.VCSTime == "" {
				v.VCSTime = s.Value
			}
		}
	}
	if len(v.Revision) > 12 {
		v.Revision = v.Revision[:12]
	}
	if v.Module == "" {
		v.Module = "(unknown)"
	}
	if v.Release == "" {
		v.Release = moduleVersion(info)
	}
	return v, true
}

func PrintVersion(w io.Writer, v VersionInfo) {
	dirty := ""
	if v.Dirty {
		dirty = "-dirty"
	}
	switch {
	case v.Release != "" && v.Revision != "":
		fmt.Fprintf(w, "figaro %s (%s%s, %s/%s, %s)\n", v.Release, v.Revision, dirty, v.OS, v.Arch, v.Go)
	case v.Release != "":
		fmt.Fprintf(w, "figaro %s (%s/%s, %s)\n", v.Release, v.OS, v.Arch, v.Go)
	case v.Revision != "":
		fmt.Fprintf(w, "figaro %s%s (%s/%s, %s)\n", v.Revision, dirty, v.OS, v.Arch, v.Go)
	default:
		fmt.Fprintf(w, "figaro unknown (%s/%s, %s)\n", v.OS, v.Arch, v.Go)
	}
	fmt.Fprintf(w, "  module:    %s\n", v.Module)
	fmt.Fprintf(w, "  exe:       %s\n", v.Exe)
	if v.VCSTime != "" {
		fmt.Fprintf(w, "  vcs.time:  %s\n", v.VCSTime)
	}
	if v.Node != "" {
		fmt.Fprintf(w, "  node:      %s\n", v.Node)
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
