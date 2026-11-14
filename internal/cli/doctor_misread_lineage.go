package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/store"
)

func runDoctorMisreadLineage(from string, jsonOut bool) error {
	if from == "" {
		from = boot.AriaRoot()
	}
	nodes, err := store.MisreadLineages(from)
	if err != nil {
		return fmt.Errorf("doctor misread-lineage: %w", err)
	}
	if jsonOut {
		if nodes == nil {
			nodes = []store.MisreadNode{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(nodes)
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tTRUNK\tKIND\tBASES\tDERIVED CHANNELS\tMETA")
	for _, n := range nodes {
		bases := make([]string, len(n.Bases))
		for i, b := range n.Bases {
			bases[i] = fmt.Sprint(b)
		}
		clears := strings.Join(n.Channels, ",")
		if clears == "" {
			clears = "-"
		}
		meta := "-"
		if n.Meta {
			meta = "refold"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", n.Node, n.Trunk, n.Kind, strings.Join(bases, ","), clears, meta)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d nodes in %s\n", len(nodes), from)
	return nil
}
