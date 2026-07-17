package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/transport"
)

func runRestWithFlags(force, keepPIDs bool) {

	sockPath := angelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if keepPIDs {
		cli, err := angelus.DialClient(ep)
		if err != nil {
			fmt.Fprintln(os.Stderr, "angelus is not running")
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
		fmt.Fprintf(os.Stderr, "persisted %d pid binding(s)\n", resp.Count)
	} else if cli, err := angelus.DialClient(ep); err == nil {
		cli.Close()
	} else {
		fmt.Fprintln(os.Stderr, "angelus is not running")
		return
	}

	pidBytes, err := os.ReadFile(filepath.Join(angelusRuntimeDir(), "angelus.pid"))
	if err != nil {
		os.Remove(sockPath)
		fmt.Fprintln(os.Stderr, "angelus pid file missing; socket removed")
		return
	}
	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		os.Remove(sockPath)
		fmt.Fprintln(os.Stderr, "angelus pid file unreadable; socket removed")
		return
	}

	if force {
		killPid(pid, syscall.SIGKILL)
		os.Remove(sockPath)
		fmt.Fprintf(os.Stderr, "angelus (pid %d) forcefully terminated\n", pid)
		return
	}

	killPid(pid, syscall.SIGTERM)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "angelus (pid %d) put to rest\n", pid)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Fprintf(os.Stderr,
		"angelus (pid %d) did not rest within 15s; try `figaro rest --force`\n", pid)
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
	fmt.Fprintf(w, "PROVIDER\tMODEL ID\tNAME\n")

	for _, name := range providerNames {
		prov, _ := buildProvider(loaded, name)
		if prov == nil {
			continue
		}
		models, err := prov.Models(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", name, err)
			continue
		}
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\t%s\n", m.Provider, m.ID, m.Name)
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
