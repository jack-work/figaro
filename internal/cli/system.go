package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

func runRestWithFlags(force, keepPIDs bool) {

	// --keep-pids says a restart is coming. The hush agent holds an
	// unlocked identity that the next daemon would otherwise have to
	// rebuild (a keyring read at best, a passphrase prompt a detached
	// daemon cannot answer at worst), so it is exactly what "keep"
	// should keep. Every other path retires it: see hushagent.go.
	retireAgent := func() {
		if keepPIDs {
			return
		}
		reportRetiredHushAgent()
	}

	sockPath := angelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if keepPIDs {
		cli, err := sdk.DialAngelus(ep)
		if err != nil {
			fmt.Fprintln(stderrw, "angelus is not running")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// TODO: server should save bindings automatically.
		resp, err := cli.SaveBindings(ctx)
		cancel()
		cli.Close()
		if err != nil {
			die("save-bindings: %s", err)
		}
		fmt.Fprintf(stderrw, "persisted %d pid binding(s)\n", resp.Count)
	} else if cli, err := sdk.DialAngelus(ep); err == nil {
		cli.Close()
	} else {
		// No daemon answering, but its agent may well still be up: an
		// agent outlives a daemon that was killed rather than stopped,
		// and that is where the orphans come from. The claim file is
		// the daemon's own record of the one it owned.
		fmt.Fprintln(stderrw, "angelus is not running")
		retireAgent()
		return
	}

	pidBytes, err := os.ReadFile(filepath.Join(angelusRuntimeDir(), "angelus.pid"))
	if err != nil {
		os.Remove(sockPath)
		fmt.Fprintln(stderrw, "angelus pid file missing; socket removed")
		return
	}
	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		os.Remove(sockPath)
		fmt.Fprintln(stderrw, "angelus pid file unreadable; socket removed")
		return
	}

	if force {
		killPid(pid, syscall.SIGKILL)
		waitForExit(pid, 5*time.Second)
		os.Remove(sockPath)
		fmt.Fprintf(stderrw, "angelus (pid %d) forcefully terminated\n", pid)
		// A SIGKILLed daemon runs no cleanup of its own, so --force is
		// the path that needs this most, not least.
		retireAgent()
		return
	}

	killPid(pid, syscall.SIGTERM)

	if waitForExit(pid, 15*time.Second) {
		fmt.Fprintf(stderrw, "angelus (pid %d) put to rest\n", pid)
		retireAgent()
		return
	}

	// The daemon is still up, and keepHushAlive would respawn anything we
	// retired within seconds. Leave the claim standing for the next stop.
	fmt.Fprintf(stderrw,
		"angelus (pid %d) did not rest within 15s; try `figaro rest --force`\n", pid)
}

// waitForExit polls until pid is gone or the deadline passes; reports whether
// it is gone.
func waitForExit(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if !pidAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// dashCount renders a token count for the models table, "-" when unknown.
func dashCount(n int) string {
	if n <= 0 {
		return "-"
	}
	return formatCtxCell(n)
}

// runModels lists provider models.
func runModels(loaded *config.Loaded) {
	ensureHush()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	providerNames := loaded.ListProviders()
	if len(providerNames) == 0 {
		// Fall back to the providers the factory knows how to build.
		providerNames = KnownProviders()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "PROVIDER\tMODEL ID\tNAME\tCONTEXT\tMAX OUT\n")

	for _, name := range providerNames {
		prov, _ := buildProvider(loaded, name)
		if prov == nil {
			continue
		}
		models, err := prov.Models(ctx)
		if err != nil {
			fmt.Fprintf(stderrw, "warning: %s: %s\n", name, err)
			continue
		}
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				m.Provider, m.ID, m.Name, dashCount(m.ContextWindow), dashCount(m.MaxTokens))
		}
	}
	w.Flush()
}

func runLoginByName(loaded *config.Loaded, providerName string) {
	reg := providerPkg.Lookup(providerName)
	if reg == nil || reg.Login == nil {
		die("no login flow for provider %q", providerName)
	}
	if err := reg.Login(loaded); err != nil {
		die("%s", err)
	}
}
