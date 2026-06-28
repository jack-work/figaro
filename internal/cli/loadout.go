// Package cli — `figaro loadout` command.
//
// Applies a named loadout additively to the current aria's
// chalkboard. The loadout file is resolved by the angelus (it
// owns the configDir); the CLI only forwards the name.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/transport"
)

// completeLoadouts completes the `loadout` command: available loadout names
// for the positional slot (sourced from the config, so it works with no aria
// attached), or aria ids after --id.
func completeLoadouts(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return softFetchAriaIDs()
	}
	loaded, _ := c.Extra.(*config.Loaded)
	if loaded == nil {
		return nil
	}
	return loaded.ListLoadouts()
}

// runLoadout calls figaro.loadout on the targeted aria.
func runLoadout(loaded *config.Loaded, ariaID, loadoutName string) {
	if loadoutName == "" {
		die("usage: figaro loadout [--id <id>] <name>")
	}

	ctx := context.Background()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false)
	if err != nil {
		die("%s", err)
	}

	fcli, err := figaro.DialClient(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address}, nil)
	if err != nil {
		die("dial aria: %s", err)
	}
	defer fcli.Close()

	resp, err := fcli.Loadout(ctx, loadoutName)
	if err != nil {
		die("loadout %q: %s", loadoutName, err)
	}

	if len(resp.Set) == 0 {
		fmt.Fprintf(os.Stderr, "loadout %q: no changes (chalkboard already matches)\n", loadoutName)
		return
	}
	fmt.Fprintf(os.Stderr, "loadout %q applied (%d keys):\n", loadoutName, len(resp.Set))
	for _, k := range resp.Set {
		fmt.Fprintf(os.Stderr, "  %s\n", k)
	}
}

// runLoadoutList prints the loadouts available on disk.
func runLoadoutList(loaded *config.Loaded) {
	names := loaded.ListLoadouts()
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "no loadouts found in", loaded.LoadoutsDir())
		return
	}
	for _, n := range names {
		marker := ""
		if n == loaded.Config.DefaultLoadout {
			marker = " (default)"
		}
		fmt.Printf("%s%s\n", n, marker)
	}
}
