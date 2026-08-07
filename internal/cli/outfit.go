// Package cli — `figaro outfit` command.
//
// Applies a named outfit additively to the current aria's
// chalkboard. The outfit file is resolved by the angelus (it
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

// completeOutfits completes the `outfit` command: available outfit names
// for the positional slot (sourced from the config, so it works with no aria
// attached), or aria ids after --id.
func completeOutfits(c *cmdkit.CompleteContext) []string {
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
	return loaded.ListOutfits()
}

// runOutfit calls figaro.outfit on the targeted aria.
func runOutfit(loaded *config.Loaded, ariaID, outfitName string) {
	if outfitName == "" {
		die("usage: figaro outfit [--id <id>] <name>")
	}

	ctx := context.Background()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false, "")
	if err != nil {
		die("%s", err)
	}

	fcli, err := figaro.DialClient(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address}, nil)
	if err != nil {
		die("dial aria: %s", err)
	}
	defer fcli.Close()

	resp, err := fcli.Outfit(ctx, outfitName)
	if err != nil {
		die("outfit %q: %s", outfitName, err)
	}

	if len(resp.Set) == 0 {
		fmt.Fprintf(os.Stderr, "outfit %q: no changes (chalkboard already matches)\n", outfitName)
		return
	}
	fmt.Fprintf(os.Stderr, "outfit %q applied (%d keys):\n", outfitName, len(resp.Set))
	for _, k := range resp.Set {
		fmt.Fprintf(os.Stderr, "  %s\n", k)
	}
}

// runOutfitList prints the outfits available on disk.
func runOutfitList(loaded *config.Loaded) {
	names := loaded.ListOutfits()
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "no outfits found in", loaded.OutfitsDir())
		return
	}
	for _, n := range names {
		marker := ""
		if n == loaded.Config.DefaultOutfit {
			marker = " (default)"
		}
		fmt.Printf("%s%s\n", n, marker)
	}
}
