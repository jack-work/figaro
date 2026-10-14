package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/update"
)

const figaroModule = "github.com/jack-work/figaro"

func updateCache() *update.Cache {
	return update.NewCache(filepath.Join(boot.CacheDir(), "figaro"))
}

// helpVersionLine reads the cache only, never the network: it runs inside the live
// pager.
func helpVersionLine() string {
	cur := update.CurrentVersion(boot.BuildRevision())
	line := "figaro " + cur
	if info := updateCache().Read(30 * 24 * time.Hour); info != nil {
		info.Current = cur
		info.Available = info.Latest != "" && update.Compare(info.Latest, cur) > 0
		if update.Nudge(info, figaroModule) != "" {
			line += " · " + info.Latest + " available: run: figaro update"
		}
	}
	return line
}

// runUpdateCheck is silent on every error path.
func runUpdateCheck(loaded *config.Loaded) {
	if loaded == nil || !loaded.CheckUpdates() {
		return
	}
	// Only nudge when stderr is a real terminal.
	if !isStderrTTY() {
		return
	}
	if update.DetectChannel() == update.ChannelDevShell {
		return
	}
	ttl := time.Duration(loaded.UpdateCheckTTLHours()) * time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info := update.Check(ctx, updateCache(), ttl, figaroModule, update.CurrentVersion(boot.BuildRevision()))
	if msg := update.Nudge(info, figaroModule); msg != "" {
		fmt.Fprintln(stderrw, msg)
	}
}

func isStderrTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// runUpdate is advisory by default: it prints versions, the channel and the
// upgrade command.
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
			fmt.Fprintln(stdout, "usage: figaro update [--check] [--json] [--apply]")
			return nil
		default:
			return fmt.Errorf("update: unknown flag %q", a)
		}
	}

	current := update.CurrentVersion(boot.BuildRevision())
	cache := updateCache()
	ttl := time.Duration(loaded.UpdateCheckTTLHours()) * time.Hour
	if force {
		ttl = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info := update.Check(ctx, cache, ttl, figaroModule, current)

	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	channel := string(info.Channel)
	fmt.Fprintf(stdout, "figaro %s installed (channel: %s)\n", info.Current, channel)
	if info.Exe != "" {
		fmt.Fprintf(stdout, "  exe:     %s\n", info.Exe)
	}
	if info.FetchError != "" {
		fmt.Fprintf(stdout, "  latest:  (unavailable: %s)\n", info.FetchError)
		return nil
	}
	fmt.Fprintf(stdout, "  latest:  %s\n", info.Latest)
	if !info.Available {
		fmt.Fprintln(stdout, "  status:  up to date  ✓")
		return nil
	}
	cmd := update.UpgradeCommand(info.Channel, figaroModule, info.Latest)
	if cmd == "" {
		fmt.Fprintln(stdout, "  status:  new release available")
		fmt.Fprintln(stdout, "  no automatic upgrade command for this install channel;")
		fmt.Fprintln(stdout, "  refer to README.md § Releasing for guidance.")
		return nil
	}
	fmt.Fprintln(stdout, "  status:  new release available")
	fmt.Fprintf(stdout, "  to upgrade: %s\n", cmd)
	if !apply {
		return nil
	}
	if info.Channel != update.ChannelGoInstall {
		return fmt.Errorf("update --apply only supported on the go-install channel (got: %s)", info.Channel)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "→ running: %s\n", cmd)
	c := exec.CommandContext(ctx, "go", "install", figaroModule+"/cmd/figaro@"+info.Latest)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("go install failed: %w", err)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "done. restart the daemon so the next command picks up the new binary:")
	fmt.Fprintln(stdout, "  figaro stop")
	return nil
}
