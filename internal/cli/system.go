package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/internal/boot"
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

	// --keep-pids says a restart is coming: the hush agent holds an unlocked
	// identity the next daemon would otherwise have to rebuild. Every other
	// path retires it.
	retireAgent := func() {
		if keepPIDs {
			return
		}
		reportRetiredHushAgent()
	}

	sockPath := boot.AngelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if keepPIDs {
		cli, err := sdk.DialAngelus(ep)
		if err != nil {
			fmt.Fprintln(stderrw, "angelus is not running")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		// An agent outlives a daemon that was killed rather than stopped; the
		// claim file is the daemon's record of the one it owned.
		fmt.Fprintln(stderrw, "angelus is not running")
		retireAgent()
		return
	}

	pidBytes, err := os.ReadFile(filepath.Join(boot.AngelusRuntimeDir(), "angelus.pid"))
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
		// A SIGKILLed daemon runs no cleanup of its own.
		retireAgent()
		return
	}

	killPid(pid, syscall.SIGTERM)

	if waitForExit(pid, 15*time.Second) {
		fmt.Fprintf(stderrw, "angelus (pid %d) put to rest\n", pid)
		retireAgent()
		return
	}

	// keepHushAlive would respawn anything retired while the daemon is up.
	fmt.Fprintf(stderrw,
		"angelus (pid %d) did not rest within 15s; try `figaro rest --force`\n", pid)
}

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

func dashCount(n int) string {
	if n <= 0 {
		return "-"
	}
	return formatCtxCell(n)
}

func runModels(loaded *config.Loaded) {
	models, warnings := fetchModels(loaded)
	for _, w := range warnings {
		fmt.Fprintf(stderrw, "warning: %s\n", w)
	}
	// The package's writer, never os.Stdout: the pager captures this one.
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "PROVIDER\tMODEL ID\tNAME\tCONTEXT\tMAX OUT\n")
	for _, m := range models {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.Provider, m.ID, m.Name, dashCount(m.ContextWindow), dashCount(m.MaxTokens))
	}
	w.Flush()
}

func runLoginByName(loaded *config.Loaded, providerName string) {
	reg := providerPkg.Lookup(providerName)
	var err error
	switch {
	case reg == nil:
		err = runEndpointLogin(loaded, providerName)
	case reg.Login != nil:
		err = reg.Login(loaded)
	case reg.OAuth != nil:
		err = runOAuthLogin(providerName, providerName, *reg.OAuth,
			"figaro set system.provider "+providerName+"; figaro set system.model <model>")
	default:
		die("no login flow for provider %q", providerName)
	}
	if err != nil {
		die("%s", err)
	}
}

func runEndpointLogin(loaded *config.Loaded, name string) error {
	e, err := boot.LoadEndpoint(loaded, name)
	if err != nil {
		return fmt.Errorf("no provider or endpoint %q", name)
	}
	if e.OAuth == nil {
		return fmt.Errorf("endpoint %q has no [oauth] to sign in with", name)
	}
	return runOAuthLogin(name, e.HushCredential(), *e.OAuth, "FIGARO_ENDPOINT="+name+" figaro ls, or [cli] endpoint = \""+name+"\"")
}
