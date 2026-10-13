// Package update surfaces new releases and the upgrade command for the
// install channel. It never mutates the binary.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type Channel string

const (
	ChannelGoInstall Channel = "go-install"
	ChannelNix       Channel = "nix"
	ChannelDevShell  Channel = "dev-shell"
	ChannelScript    Channel = "script"
	ChannelHomebrew  Channel = "homebrew"
	ChannelUnknown   Channel = "unknown"
)

// ChannelMarker is the one-word file an installer drops beside the binary;
// a wrong channel guess prints an upgrade command that damages the install.
const ChannelMarker = ".figaro-channel"

// Info is the result of a check; Latest is empty on failure.
type Info struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest"`
	Available  bool      `json:"available"`
	Channel    Channel   `json:"channel"`
	Exe        string    `json:"exe"`
	CheckedAt  time.Time `json:"checked_at"`
	FetchError string    `json:"fetch_error,omitempty"`
}

func DetectChannel() Channel {
	exe, err := os.Executable()
	if err != nil {
		return ChannelUnknown
	}
	return channelFor(exe)
}

func channelFor(exe string) Channel {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.HasPrefix(exe, "/nix/store/") {
		if os.Getenv("FIGARO_DEV_ROOT") != "" {
			return ChannelDevShell
		}
		return ChannelNix
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), ChannelMarker)); err == nil {
		switch ch := Channel(strings.TrimSpace(string(b))); ch {
		case ChannelScript, ChannelHomebrew, ChannelGoInstall, ChannelNix:
			return ch
		}
	}
	// The Cellar segment holds for a custom brew prefix too.
	if strings.Contains(exe, string(os.PathSeparator)+"Cellar"+string(os.PathSeparator)) {
		return ChannelHomebrew
	}
	// No `go env`: the toolchain may not be installed.
	if gobin := os.Getenv("GOBIN"); gobin != "" && strings.HasPrefix(exe, gobin+string(os.PathSeparator)) {
		return ChannelGoInstall
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			gopath = filepath.Join(home, "go")
		}
	}
	if gopath != "" {
		binDir := filepath.Join(gopath, "bin")
		if strings.HasPrefix(exe, binDir+string(os.PathSeparator)) {
			return ChannelGoInstall
		}
	}
	return ChannelUnknown
}

// CurrentVersion is the module version, "dev-<rev>" for a stamped worktree
// build, or "(devel)".
func CurrentVersion(commitFallback string) string {
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	if commitFallback != "" {
		if len(commitFallback) > 12 {
			commitFallback = commitFallback[:12]
		}
		return "dev-" + commitFallback
	}
	if ok {
		return info.Main.Version
	}
	return "(unknown)"
}

// FetchLatest asks proxy.golang.org for the module's latest tag.
func FetchLatest(ctx context.Context, module string) (string, error) {
	url := "https://proxy.golang.org/" + module + "/@latest"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", fmt.Errorf("proxy returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Version string `json:"Version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Version == "" {
		return "", fmt.Errorf("proxy returned empty version")
	}
	return payload.Version, nil
}

// Compare orders vMAJOR.MINOR.PATCH strings; unparseable input is -inf so an
// unknown never claims to be newer.
func Compare(a, b string) int {
	am, ap, ao, aok := parseSemver(a)
	bm, bp, bo, bok := parseSemver(b)
	if !aok && !bok {
		return 0
	}
	if !aok {
		return -1
	}
	if !bok {
		return +1
	}
	if am != bm {
		return sign(am - bm)
	}
	if ap != bp {
		return sign(ap - bp)
	}
	if ao != bo {
		return sign(ao - bo)
	}
	as := suffix(a)
	bs := suffix(b)
	switch {
	case as == "" && bs != "":
		return +1
	case as != "" && bs == "":
		return -1
	case as < bs:
		return -1
	case as > bs:
		return +1
	}
	return 0
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

func parseSemver(s string) (int, int, int, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

func suffix(s string) string {
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		return s[i:]
	}
	return ""
}

// UpgradeCommand is "" when the channel has no suggestion.
func UpgradeCommand(ch Channel, module, latest string) string {
	switch ch {
	case ChannelGoInstall:
		v := latest
		if v == "" {
			v = "latest"
		}
		return "go install " + module + "/cmd/figaro@" + v
	case ChannelNix:
		return "nix profile upgrade figaro   # or your flake input, then: nix profile upgrade '.*'"
	case ChannelDevShell:
		return "git pull && exit && nix develop   # (dev shell: rebuild by re-entering)"
	case ChannelScript:
		return "curl -fsSL https://figar.org/install.sh | sh"
	case ChannelHomebrew:
		return "brew upgrade figaro"
	}
	return ""
}
