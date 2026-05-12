package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/transport"
)

// runRest puts the angelus to rest.
func runRest() {
	force := false
	keepPIDs := false
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--force", "-f":
			force = true
		case "--keep-pids", "-k":
			keepPIDs = true
		}
	}
	runRestWithFlags(force, keepPIDs)
}

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
		syscall.Kill(pid, syscall.SIGKILL)
		os.Remove(sockPath)
		fmt.Fprintf(os.Stderr, "angelus (pid %d) forcefully terminated\n", pid)
		return
	}

	syscall.Kill(pid, syscall.SIGTERM)

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	providerNames := loaded.ListProviders()
	if len(providerNames) == 0 {
		providerNames = []string{loaded.Config.DefaultProvider}
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

// runLogin runs the OAuth flow.
func runLogin(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro login <provider>")
	}
	runLoginByName(loaded, os.Args[2])
}

func runLoginByName(loaded *config.Loaded, providerName string) {
	h := mustHush()
	hushClient := h.Client()

	var oauthCfg auth.OAuthConfig
	switch providerName {
	case "anthropic":
		oauthCfg = auth.AnthropicOAuth
	default:
		die("no OAuth config for provider %q", providerName)
	}

	err := auth.Login(hushClient, oauthCfg, func() (string, error) {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		return strings.TrimSpace(line), err
	})
	if err != nil {
		die("%s", err)
	}
}
