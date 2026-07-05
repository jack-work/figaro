// Package update surfaces available new releases of figaro and helps
// the user apply the upgrade against whichever install channel they
// chose. It is intentionally humble: it never mutates the binary on
// its own, it only *nudges*.
//
// The check pings proxy.golang.org for the latest tag on
// github.com/jack-work/figaro. That module proxy is what `go install`
// already trusts and it needs no auth or GitHub-API rate limit dance.
// The result is cached on disk with a TTL so repeated CLI invocations
// stay quiet.
//
// Detection of the install channel is best-effort: os.Executable()
// under /nix/store means Nix; under $(go env GOPATH)/bin (or one that
// looks like it) means `go install`; otherwise "unknown", which
// downgrades any actionable suggestion to a generic advisory.
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

// Channel names the install medium figaro is running under.
type Channel string

const (
	ChannelGoInstall Channel = "go-install"
	ChannelNix       Channel = "nix"
	ChannelDevShell  Channel = "dev-shell"
	ChannelUnknown   Channel = "unknown"
)

// Info is the result of a check. Latest is empty on failure; Available
// is true iff Latest is strictly greater than Current.
type Info struct {
	Current    string    `json:"current"`   // module version of the running binary, e.g. "v0.1.4" or "(devel)"
	Latest     string    `json:"latest"`    // latest tag on the module proxy, "" if the check failed
	Available  bool      `json:"available"` // true iff Latest > Current
	Channel    Channel   `json:"channel"`
	Exe        string    `json:"exe"`
	CheckedAt  time.Time `json:"checked_at"`
	FetchError string    `json:"fetch_error,omitempty"`
}

// DetectChannel returns the install channel of the running binary.
func DetectChannel() Channel {
	exe, err := os.Executable()
	if err != nil {
		return ChannelUnknown
	}
	// Resolve one level of symlink (Nix wrappers, ~/.nix-profile, …)
	// so /nix/store detection is robust.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.HasPrefix(exe, "/nix/store/") {
		// A dev-shell build under a worktree still lives in /nix/store
		// (the flake output). Differentiate by the presence of the
		// dev-shell marker envs.
		if os.Getenv("FIGARO_DEV_ROOT") != "" {
			return ChannelDevShell
		}
		return ChannelNix
	}
	// Heuristic: $GOBIN or $GOPATH/bin. We don't shell out to `go env`
	// (adds a dependency on the go toolchain being installed); we sniff
	// the env directly with fallbacks.
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

// CurrentVersion returns the semver-ish version stamped on the module,
// or "(devel)" for `go run`, or a short git rev for -ldflags-stamped
// worktree builds.
//
// commitFallback is the ldflags-injected VCS revision from
// internal/cli.version.go — pass it in so we don't create a cycle.
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
		return info.Main.Version // "(devel)" or ""
	}
	return "(unknown)"
}

// FetchLatest asks proxy.golang.org for the latest tagged version of
// the module. Uses the public /@latest endpoint, which returns JSON
// like {"Version":"v0.1.4","Time":"..."}. No auth, no rate limits
// that matter for a once-a-day poll.
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

// Compare returns +1 if a > b, -1 if a < b, 0 if equal. Both are
// expected to be "vMAJOR.MINOR.PATCH" (optional pre-release suffix
// tolerated by falling back to string compare on that segment). Any
// unparseable input is treated as "-inf" so unknowns never spuriously
// claim to be newer than the proxy answer.
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
	// Equal numeric core: shorter pre-release string > longer (v1.0.0 > v1.0.0-rc1).
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

// parseSemver returns (major, minor, patch, ok). Accepts "v" prefix.
func parseSemver(s string) (int, int, int, bool) {
	s = strings.TrimPrefix(s, "v")
	// strip pre-release / build metadata for numeric compare
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

// UpgradeCommand returns the shell command the user should run to
// upgrade, given the install channel. Empty means "no automatic
// suggestion possible on this channel".
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
		return "git pull && exit && nix develop   # (dev shell — rebuild by re-entering)"
	}
	return ""
}
