package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"strconv"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/config"
)

const (
	fieldUsed     = "used"
	fieldChildren = "children"
	fieldFate     = "fate"
)

var gcColors = []figtree.FieldColor{{
	FieldName: fieldUsed,
	Rules: []figtree.ColorRule{
		{Value: "yes", Color: "present"},
		{Value: "no", Color: "absent"},
	},
}}

func runGC(loaded *config.Loaded, dryRun, jsonOut bool) {
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		resp, err := acli.GC(ctx, dryRun)
		if err != nil {
			die("gc: %s", err)
		}

		if jsonOut {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(resp); err != nil {
				die("gc --json: %s", err)
			}
			return nil
		}

		printStumps(*resp)
		printFormHistory(*resp)
		return nil
	})
}

func printStumps(resp rpc.GCResponse) {
	if len(resp.Stumps) == 0 {
		fmt.Fprintln(stderrw, "no outfit versions in this store")
		return
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
	fmt.Fprint(stdout, tree.Render(listOutputWidth()))

	fmt.Fprintln(stderrw)
	switch {
	case resp.DryRun && resp.Collected == 0:
		fmt.Fprintf(stderrw, "%d version(s), none collectable\n", len(resp.Stumps))
	case resp.DryRun:
		fmt.Fprintf(stderrw, "%d of %d version(s) would be collected · run `figaro gc` to do it\n",
			resp.Collected, len(resp.Stumps))
	default:
		fmt.Fprintf(stderrw, "collected %d of %d version(s)\n", resp.Collected, len(resp.Stumps))
	}
}

const (
	fieldPins     = "pins"
	fieldSegments = "segments"
	fieldSize     = "size"
)

func printFormHistory(resp rpc.GCResponse) {
	fmt.Fprintln(stderrw)
	if resp.FormsErr != "" {
		fmt.Fprintf(stderrw, "form history: %s\n", resp.FormsErr)
	}
	if resp.Retention == string(config.RetentionOff) {
		fmt.Fprintln(stderrw, "form history: retention is off ([store] gc)")
		return
	}
	if len(resp.Forms) == 0 {
		fmt.Fprintln(stderrw, "form history: nothing to drop")
		return
	}
	tree := figtree.Tree{Columns: []figtree.Column{
		{Header: "FORM"},
		{Header: "PINS", Field: fieldPins},
		{Header: "SEGMENTS", Field: fieldSegments},
		{Header: "SIZE", Field: fieldSize},
	}}
	var segments int
	var bytes int64
	for _, f := range resp.Forms {
		segments += f.Segments
		bytes += f.Bytes
		tree.Roots = append(tree.Roots, &figtree.Node{Marker: "○", Label: f.ID, Fields: map[string]string{
			fieldPins:     strconv.Itoa(f.Pins),
			fieldSegments: strconv.Itoa(f.Segments),
			fieldSize:     humanBytes(f.Bytes),
		}})
	}
	fmt.Fprint(stdout, tree.Render(listOutputWidth()))
	fmt.Fprintln(stderrw)
	what := fmt.Sprintf("%d segment(s), %s in %d form(s)", segments, humanBytes(bytes), len(resp.Forms))
	switch {
	case resp.FormsPlanned && !resp.DryRun:
		fmt.Fprintf(stderrw, "form history: %s would be dropped · [store] gc is %q; set it to \"on\" to drop them\n", what, resp.Retention)
	case resp.FormsPlanned:
		fmt.Fprintf(stderrw, "form history: %s would be dropped\n", what)
	default:
		fmt.Fprintf(stderrw, "form history: dropped %s\n", what)
	}
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
