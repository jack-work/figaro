package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/update"
)

// figaroModule is the module path used both to ask the module proxy
// for the latest tag and to construct the go-install suggestion.
const figaroModule = "github.com/jack-work/figaro"

// updateCache returns a cache handle rooted at the CLI's cacheDir().
func updateCache() *update.Cache {
	return update.NewCache(filepath.Join(cacheDir(), "figaro"))
}

// helpVersionLine is the version line for the transcript's '?' panel:
// "figaro <version>", extended with the update nudge when the CACHE
// (only — never the network; this runs inside the live pager) says a
// newer release is comparable and available. The passive nudge lives in
// the help surfaces exactly so it never interleaves with real output.
func helpVersionLine() string {
	cur := update.CurrentVersion(commit)
	line := "figaro " + cur
	if info := updateCache().Read(30 * 24 * time.Hour); info != nil {
		info.Current = cur
		info.Available = info.Latest != "" && update.Compare(info.Latest, cur) > 0
		if update.Nudge(info, figaroModule) != "" {
			line += " · " + info.Latest + " available — run: figaro update"
		}
	}
	return line
}

// runUpdateCheck is the help-surface update nudge (figaro help /
// --help). It prints a single stderr line if a newer release is
// available and the config allows it. All error paths are silent — a
// failed update check must never interfere with real CLI work.
func runUpdateCheck(loaded *config.Loaded) {
	if loaded == nil || !loaded.CheckUpdates() {
		return
	}
	// Only nudge when stderr is a real terminal. Scripts and pipes
	// stay quiet; they can call `figaro update` on demand.
	if !isStderrTTY() {
		return
	}
	// Dev-shell builds churn faster than any release cadence — skip.
	if update.DetectChannel() == update.ChannelDevShell {
		return
	}
	ttl := time.Duration(loaded.UpdateCheckTTLHours()) * time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info := update.Check(ctx, updateCache(), ttl, figaroModule, update.CurrentVersion(commit))
	if msg := update.Nudge(info, figaroModule); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
}

// isStderrTTY reports whether stderr is a character device (terminal).
func isStderrTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// runUpdate implements `figaro update`. Default behavior is advisory:
// print the current version, the latest available version, the install
// channel, and the exact command the user should run to upgrade.
//
//	--check   force a network check (ignore the cache)
//	--json    machine-readable output
//	--apply   only meaningful on go-install channel: shell out to
//	          `go install ...@<latest>` and report the result.
func runUpdate(loaded *config.Loaded, args []string) error {
	var (
		force  bool
		asJSON bool
		apply  bool
	)
	for _, a := range args {
		switch a {
		case "-c", "--check":
			force = true
		case "-j", "--json":
			asJSON = true
		case "--apply":
			apply = true
		case "-h", "--help":
			fmt.Println("usage: figaro update [--check] [--json] [--apply]")
			return nil
		default:
			return fmt.Errorf("update: unknown flag %q", a)
		}
	}

	current := update.CurrentVersion(commit)
	cache := updateCache()
	ttl := time.Duration(loaded.UpdateCheckTTLHours()) * time.Hour
	if force {
		ttl = 0 // bypass cache
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info := update.Check(ctx, cache, ttl, figaroModule, current)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	channel := string(info.Channel)
	fmt.Printf("figaro %s installed (channel: %s)\n", info.Current, channel)
	if info.Exe != "" {
		fmt.Printf("  exe:     %s\n", info.Exe)
	}
	if info.FetchError != "" {
		fmt.Printf("  latest:  (unavailable: %s)\n", info.FetchError)
		return nil
	}
	fmt.Printf("  latest:  %s\n", info.Latest)
	if !info.Available {
		fmt.Println("  status:  up to date  ✓")
		return nil
	}
	cmd := update.UpgradeCommand(info.Channel, figaroModule, info.Latest)
	if cmd == "" {
		fmt.Println("  status:  new release available")
		fmt.Println("  no automatic upgrade command for this install channel;")
		fmt.Println("  refer to README.md § Releasing for guidance.")
		return nil
	}
	fmt.Println("  status:  new release available")
	fmt.Printf("  to upgrade: %s\n", cmd)
	if !apply {
		return nil
	}
	if info.Channel != update.ChannelGoInstall {
		return fmt.Errorf("update --apply only supported on the go-install channel (got: %s)", info.Channel)
	}
	fmt.Println()
	fmt.Printf("→ running: %s\n", cmd)
	c := exec.CommandContext(ctx, "go", "install", figaroModule+"/cmd/figaro@"+info.Latest)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("go install failed: %w", err)
	}
	fmt.Println()
	fmt.Println("done. restart the daemon so the next command picks up the new binary:")
	fmt.Println("  figaro stop")
	return nil
}
