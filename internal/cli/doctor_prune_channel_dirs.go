package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func runDoctorPruneChannelDirs(dryRun, jsonOut bool) error {
	if cli, err := sdk.DialAngelus(doorEndpoint()); err == nil {
		cli.Close()
		return fmt.Errorf("angelus is running; stop it first (figaro stop)")
	}
	be, err := store.NewXwalBackend(boot.AriaRoot(), 0)
	if err != nil {
		return err
	}
	defer be.Close()
	rep, err := be.PruneEmptyChannelDirs(dryRun)
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	if rep.Skipped != "" {
		fmt.Fprintf(stdout, "skipped: %s\n", rep.Skipped)
		return nil
	}
	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "CHANNEL\tSHAPE\t%s\tKEPT\n", map[bool]string{true: "WOULD REMOVE", false: "REMOVED"}[dryRun])
	var removed, kept int
	for _, c := range rep.Counts {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", c.Channel, c.Shape, c.Removed, c.Kept)
		removed += c.Removed
		kept += c.Kept
	}
	w.Flush()
	fmt.Fprintf(stdout, "%s %d channel directories, kept %d\n", verb, removed, kept)
	return nil
}
