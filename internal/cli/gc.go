// Package cli: `figaro gc` command.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/api/rpc"
)

// Fields the gc report reads.
const (
	fieldUsed     = "used"
	fieldChildren = "children"
	fieldFate     = "fate"
)

// gcColors: a stump still hosting arias is green, one about to go is red: the
// same reading as an outfit closure, where red is what is not there (or, here,
// what is about to stop being).
var gcColors = []figtree.FieldColor{{
	FieldName: fieldUsed,
	Rules: []figtree.ColorRule{
		{Value: "yes", Color: "present"},
		{Value: "no", Color: "absent"},
	},
}}

func runGC(loaded *config.Loaded, dryRun, jsonOut bool) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		resp, err := acli.GC(ctx, dryRun)
		if err != nil {
			die("gc: %s", err)
		}

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(resp); err != nil {
				die("gc --json: %s", err)
			}
			return nil
		}

		if len(resp.Stumps) == 0 {
			fmt.Fprintln(os.Stderr, "no outfit stumps in this store")
			return nil
		}

		tree := figtree.Tree{
			Columns: []figtree.Column{
				{Header: "OUTFIT"},
				{Header: "VERSION", Field: fieldVer},
				{Header: "ARIAS", Field: fieldChildren},
				{Header: "FATE", Field: fieldFate},
			},
			Colors: gcColors,
		}
		for _, st := range resp.Stumps {
			tree.Roots = append(tree.Roots, gcNode(st, resp.DryRun))
		}
		fmt.Fprint(os.Stdout, tree.Render(listOutputWidth()))

		fmt.Fprintln(os.Stderr)
		switch {
		case resp.DryRun && resp.Collected == 0:
			fmt.Fprintf(os.Stderr, "%d stump(s), none collectable\n", len(resp.Stumps))
		case resp.DryRun:
			fmt.Fprintf(os.Stderr, "%d of %d stump(s) would be collected · run `figaro gc` to do it\n",
				resp.Collected, len(resp.Stumps))
		default:
			fmt.Fprintf(os.Stderr, "collected %d of %d stump(s)\n", resp.Collected, len(resp.Stumps))
		}
		return nil
	})
}

func gcNode(st rpc.GCStump, dryRun bool) *figtree.Node {
	used, marker, fate := "yes", "●", "kept"
	if st.Children == 0 {
		used, marker = "no", "○"
		fate = "collected"
		if dryRun {
			fate = "would collect"
		}
	}
	if st.Err != "" {
		used, marker, fate = "yes", "!", "failed: "+st.Err
	}
	label := st.Outfit
	if label == "" {
		label = st.ID
	}
	return &figtree.Node{
		Marker: marker,
		Label:  label,
		Fields: map[string]string{
			fieldVer:      dash(st.Version),
			fieldChildren: strconv.Itoa(st.Children),
			fieldFate:     fate,
			fieldUsed:     used,
		},
	}
}
