package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk"
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

type gcOpts struct {
	sel    rpc.GCSelector
	yes    bool
	dryRun bool
	asJSON bool
	undo   string
}

type gcEndpoint interface {
	GCPlan(ctx context.Context, sel rpc.GCSelector) (*rpc.GCPlanResponse, error)
	GCApply(ctx context.Context, run string) (*rpc.GCApplyResponse, error)
	GCUndo(ctx context.Context, run string) (*rpc.GCUndoResponse, error)
}

type gcTerm struct {
	out, err    io.Writer
	in          io.Reader
	interactive bool
}

func gcOptsOf(ctx *cmdkit.RunContext) (gcOpts, error) {
	o := gcOpts{
		yes:    ctx.BoolFlag("yes"),
		dryRun: ctx.BoolFlag("dry-run"),
		asJSON: ctx.BoolFlag("json"),
		undo:   ctx.Flag("undo"),
		sel: rpc.GCSelector{
			Empty:  ctx.BoolFlag("empty"),
			Outfit: ctx.Flag("outfit"),
			Under:  ctx.Flag("under"),
			Match:  ctx.Flag("match"),
			Box:    ctx.Flag("box"),
		},
	}
	for flag, into := range map[string]*int64{"idle": &o.sel.IdleMS, "older": &o.sel.OlderMS} {
		raw := ctx.Flag(flag)
		if raw == "" {
			continue
		}
		d, err := store.ParseTTL(raw)
		if err != nil || d <= 0 {
			return o, fmt.Errorf("--%s %q: give a duration such as 30d, 12h or 2w", flag, raw)
		}
		*into = d.Milliseconds()
	}
	if o.undo != "" && (o.sel != rpc.GCSelector{} || o.yes || o.dryRun) {
		return o, errors.New("--undo takes only a run id")
	}
	if o.yes && o.dryRun {
		return o, errors.New("--yes and --dry-run disagree")
	}
	return o, nil
}

func runGCCommand(loaded *config.Loaded, o gcOpts) {
	t := gcTerm{
		out: stdout, err: stderrw, in: os.Stdin,
		interactive: term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())),
	}
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return runGC(ctx, acli, o, t)
	})
}

func runGC(ctx context.Context, d gcEndpoint, o gcOpts, t gcTerm) error {
	if o.undo != "" {
		resp, err := d.GCUndo(ctx, o.undo)
		if err != nil {
			return fmt.Errorf("gc --undo: %w", err)
		}
		if o.asJSON {
			return writeJSON(t.out, resp)
		}
		fmt.Fprintf(t.err, "restored %d figaro(s), %d node(s), reaped by run %s\n", len(resp.Restored), resp.Nodes, resp.Run)
		return nil
	}
	plan, err := d.GCPlan(ctx, o.sel)
	if err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	if o.asJSON && !o.yes {
		return writeJSON(t.out, plan)
	}
	stumpsOnly := o.sel == rpc.GCSelector{}
	if !o.asJSON {
		printGCPlan(t, plan, stumpsOnly)
	}
	if len(plan.Candidates) == 0 && collectable(plan.Stumps) == 0 {
		return nil
	}
	if o.dryRun {
		return nil
	}
	if !o.yes {
		if !t.interactive {
			fmt.Fprintf(t.err, "plan only, nothing changed: pass --yes to apply it\n")
			return nil
		}
		fmt.Fprintf(t.out, "%s? [y/N] ", gcQuestion(plan))
		line, _ := bufio.NewReader(t.in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(t.err, "nothing changed")
			return nil
		}
	}
	res, err := d.GCApply(ctx, plan.Run)
	if err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	if o.asJSON {
		if err := writeJSON(t.out, res); err != nil {
			return err
		}
	} else {
		printGCResult(t, res, stumpsOnly)
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("gc: %d failed", len(res.Failed))
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func collectable(stumps []rpc.GCStump) int {
	n := 0
	for _, s := range stumps {
		if s.Children == 0 {
			n++
		}
	}
	return n
}

func gcQuestion(plan *rpc.GCPlanResponse) string {
	var parts []string
	if n := len(plan.Candidates); n > 0 {
		parts = append(parts, fmt.Sprintf("reap %d figaro(s), %d node(s)", n, plan.Nodes))
	}
	if n := collectable(plan.Stumps); n > 0 {
		parts = append(parts, fmt.Sprintf("collect %d outfit version(s)", n))
	}
	return strings.Join(parts, " and ")
}

func gcName(id, words string) string {
	if words != "" {
		return words
	}
	return ariaName(id)
}

func gcIdle(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= 48*time.Hour {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return shortDuration(d)
}

func printGCPlan(t gcTerm, plan *rpc.GCPlanResponse, stumpsOnly bool) {
	if stumpsOnly {
		printStumps(t.out, t.err, plan.Stumps, true)
		return
	}
	if len(plan.Candidates) > 0 {
		w := tabwriter.NewWriter(t.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "REAP\tIDLE\tMSGS\tSIZE\tMANTRA")
		for _, c := range plan.Candidates {
			name := gcName(c.ID, c.Handle)
			if n := len(c.Takes); n > 0 {
				name += fmt.Sprintf(" +%d", n)
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", name, gcIdle(c.IdleMS), c.Messages, humanBytes(c.Bytes), truncRunes(c.Mantra, 48))
		}
		_ = w.Flush()
	}
	if len(plan.Kept) > 0 {
		if len(plan.Candidates) > 0 {
			fmt.Fprintln(t.out)
		}
		w := tabwriter.NewWriter(t.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "KEPT\tWHY\tMANTRA")
		for _, k := range plan.Kept {
			fmt.Fprintf(w, "%s\t%s\t%s\n", gcName(k.ID, k.Handle), k.Why, truncRunes(k.Mantra, 40))
		}
		_ = w.Flush()
	}
	if len(plan.Candidates) == 0 {
		fmt.Fprintf(t.err, "nothing to reap; %d kept\n", len(plan.Kept))
		return
	}
	purge := "at purge"
	if plan.GraceMS > 0 {
		purge = "at purge, " + gcIdle(plan.GraceMS) + " after the reap"
	}
	fmt.Fprintf(t.err, "\n%d figaro(s), %d node(s), %s freed %s; %d kept · run %s\n",
		len(plan.Candidates), plan.Nodes, humanBytes(plan.Bytes), purge, len(plan.Kept), plan.Run)
}

func printGCResult(t gcTerm, res *rpc.GCApplyResponse, stumpsOnly bool) {
	for _, d := range res.Dropped {
		fmt.Fprintf(t.err, "dropped %s: %s\n", gcName(d.ID, d.Handle), d.Why)
	}
	for _, d := range res.Failed {
		fmt.Fprintf(t.err, "failed %s: %s\n", gcName(d.ID, d.Handle), d.Why)
	}
	if res.Collected > 0 {
		fmt.Fprintf(t.err, "collected %d outfit version(s)\n", res.Collected)
	}
	if stumpsOnly {
		return
	}
	fmt.Fprintf(t.err, "reaped %d figaro(s), %d node(s), %s · figaro reaped lists them · undo: figaro gc --undo %s\n",
		len(res.Reaped), res.Nodes, humanBytes(res.Bytes), res.Run)
}

func printStumps(out, errw io.Writer, stumps []rpc.GCStump, planned bool) {
	if len(stumps) == 0 {
		fmt.Fprintln(errw, "no outfit versions in this store")
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
	for _, st := range stumps {
		tree.Roots = append(tree.Roots, gcNode(st, planned))
	}
	fmt.Fprint(out, tree.Render(listOutputWidth()))
	fmt.Fprintln(errw)
	n := collectable(stumps)
	if n == 0 {
		fmt.Fprintf(errw, "%d version(s), none collectable\n", len(stumps))
		return
	}
	fmt.Fprintf(errw, "%d of %d version(s) collectable\n", n, len(stumps))
}

func gcNode(st rpc.GCStump, planned bool) *figtree.Node {
	used, marker, fate := "yes", "●", "kept"
	if st.Children == 0 {
		used, marker = "no", "○"
		fate = "collected"
		if planned {
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
