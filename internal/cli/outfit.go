// Package cli — `figaro state outfit`.
//
// Applies a spec additively to the current aria's chalkboard. Names are
// resolved by the aria (the daemon owns the configDir); the CLI parses the
// syntax, so a typo costs no round trip, and forwards JSON.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	jkrpc "github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// runStateOutfit is `figaro state outfit …`: the apply, the closure and the
// listing. It is a verb rather than a flag because dressing state is an
// action; the same fold reaches send/new/fork through -O.
func runStateOutfit(loaded *config.Loaded, ctx *cmdkit.RunContext, args []string) error {
	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	if len(args) > 1 {
		return fmt.Errorf("one spec, comma-separated: `state outfit %s`", strings.Join(args, ","))
	}
	switch {
	case ctx.BoolFlag("refresh"):
		runOutfitRefresh(loaded)
	case ctx.BoolFlag("list"):
		runOutfitList(loaded)
	case ctx.BoolFlag("tree"):
		runOutfitTree(loaded, arg)
	case arg == "":
		return fmt.Errorf("usage: figaro state outfit [--id <id>] <spec>")
	default:
		runOutfit(loaded, ctx.Flag("id"), arg)
	}
	return nil
}

// completeStateArgs offers the `outfit` sub-verb first, then whatever that
// position wants: aria ids for the snapshot form, outfit names after `outfit`.
func completeStateArgs(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	for _, a := range c.Args {
		if a == "outfit" {
			return completeOutfits(c)
		}
	}
	return append([]string{"outfit"}, completeAriaIDsPositionalOrFlag(c)...)
}

// completeOutfits completes `state outfit`: available outfit names for the
// positional slot (sourced from the config, so it works with no aria
// attached), or aria ids after --id. A comma-separated list completes its last
// segment, so `figaro state outfit a,<TAB>` offers the rest.
func completeOutfits(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return softFetchAriaIDs()
	}
	names := softFetchOutfitNames()
	if len(names) == 0 {
		return nil
	}
	prefix := ""
	if i := strings.LastIndex(c.Current, ","); i >= 0 {
		prefix = c.Current[:i+1]
	}
	if prefix == "" {
		return names
	}
	chosen := map[string]bool{}
	if names, err := outfit.TermNames(prefix); err == nil {
		for _, n := range names {
			chosen[n] = true
		}
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !chosen[n] {
			out = append(out, prefix+n)
		}
	}
	return out
}

// runOutfit dresses the targeted aria: `-O`'s syntax parsed into a patch and
// applied like any other. The server resolves the layers it names.
func runOutfit(loaded *config.Loaded, ariaID, arg string) {
	d := mustParseDressing(arg, "figaro state outfit [--id <id>] <spec>")

	ctx := context.Background()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		die("%s", err)
	}

	fcli, err := figaro.DialClient(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address}, nil)
	if err != nil {
		die("dial aria: %s", err)
	}
	defer fcli.Close()

	resp, err := fcli.Set(ctx, *d.patch, 0)
	if err != nil {
		dieOutfitFailure(d.label(), err)
	}
	if len(resp.Set) == 0 {
		fmt.Fprintf(os.Stderr, "outfit %s: no changes (chalkboard already matches)\n", d.label())
		return
	}
	fmt.Fprintf(os.Stderr, "outfit %s applied (%d keys):\n", d.label(), len(resp.Set))
	for _, k := range resp.Set {
		fmt.Fprintf(os.Stderr, "  %s\n", k)
	}
}

// dieOutfitFailure reports a failed apply.
func dieOutfitFailure(label string, err error) {
	dieWithClosure(err, "outfit %s: %s", label, err)
}

// dieWithClosure reports err, drawing the outfit layer closure when the server
// sent one. The shape is the explanation: every outfit that was found is green
// and every one that was not is red, so a broken reference several layers down
// is located rather than merely named. Without a closure it is an ordinary die.
func dieWithClosure(err error, format string, args ...any) {
	if !reportClosure(err, format, args...) {
		die(format, args...)
	}
	exitNow(1)
}

// reportClosure prints err with its closure tree when it carries one, and
// reports whether it did. Callers that return an exit code rather than dying
// use it directly.
func reportClosure(err error, format string, args ...any) bool {
	closure := outfitClosureFrom(err)
	if closure == nil {
		return false
	}
	fmt.Fprintf(os.Stderr, "error: "+format+"\n\n", args...)
	fmt.Fprint(os.Stderr, renderOutfitClosure(closure))
	fmt.Fprintln(os.Stderr)
	return true
}

// outfitClosureFrom digs the layer closure out of a typed RPC error.
func outfitClosureFrom(err error) *rpc.OutfitLayer {
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) || len(jerr.Data) == 0 {
		return nil
	}
	var data rpc.ErrorData
	if json.Unmarshal(jerr.Data, &data) != nil {
		return nil
	}
	return data.OutfitClosure
}

// Closure field names, and the colour rules read off them.
const (
	fieldFound = "found"
	fieldPath  = "path"
)

// outfitClosureColors paints a found outfit with the diff renderer's green and
// a missing one with its red.
var outfitClosureColors = []figtree.FieldColor{{
	FieldName: fieldFound,
	Rules: []figtree.ColorRule{
		{Value: "yes", Color: "present"},
		{Value: "no", Color: "absent"},
	},
}}

// renderOutfitClosure draws the closure. The synthetic root — the one with no
// name, holding several requested outfits side by side — is not drawn; its
// children become the roots, so `figaro outfit a,b` shows two trees.
func renderOutfitClosure(root *rpc.OutfitLayer) string {
	tree := figtree.Tree{
		Columns: []figtree.Column{
			{Header: "OUTFIT"},
			{Header: "LAYERS FROM", Field: fieldPath},
		},
		Colors: outfitClosureColors,
	}
	for _, node := range outfitClosureRoots(root) {
		tree.Roots = append(tree.Roots, outfitClosureNode(node))
	}
	return tree.Render(listOutputWidth())
}

func outfitClosureRoots(root *rpc.OutfitLayer) []*rpc.OutfitLayer {
	if root == nil {
		return nil
	}
	if root.Name == "" {
		return root.Layers
	}
	return []*rpc.OutfitLayer{root}
}

func outfitClosureNode(l *rpc.OutfitLayer) *figtree.Node {
	found, where := "no", "not found"
	switch {
	case l.Cycle:
		found, where = "no", "cycle: already a layer above this one"
	case l.Found:
		found, where = "yes", l.Path
	}
	n := &figtree.Node{
		Marker: outfitClosureMarker(l),
		Label:  l.Name,
		Fields: map[string]string{fieldFound: found, fieldPath: where},
	}
	for _, child := range l.Layers {
		n.Children = append(n.Children, outfitClosureNode(child))
	}
	return n
}

func outfitClosureMarker(l *rpc.OutfitLayer) string {
	if l.Found && !l.Cycle {
		return "✓"
	}
	return "✗"
}

// runOutfitTree prints an outfit's layer closure without applying anything.
//
// The angelus resolves it: the outfits directory is the server's state. Exit
// status follows the closure — 0 when every layer was found, 1 when the picture
// has red in it, so it can gate a script.
func runOutfitTree(loaded *config.Loaded, arg string) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	resp, err := acli.Outfits(context.Background(), arg)
	if err != nil {
		dieWithClosure(err, "state outfit --tree: %s", err)
	}
	if resp.Closure == nil {
		die("state outfit --tree: name an outfit, or set a default with the first-run flow")
	}
	fmt.Print(renderOutfitClosure(resp.Closure))
	if broken := outfitClosureBroken(resp.Closure); len(broken) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d unresolved: %s\n", len(broken), strings.Join(broken, ", "))
		exitNow(1)
	}
}

// outfitClosureBroken names every node that is missing or cyclic.
func outfitClosureBroken(l *rpc.OutfitLayer) []string {
	if l == nil {
		return nil
	}
	var out []string
	if l.Name != "" && (!l.Found || l.Cycle) {
		out = append(out, l.Name)
	}
	for _, child := range l.Layers {
		out = append(out, outfitClosureBroken(child)...)
	}
	return out
}

// runOutfitRefresh tells the angelus to re-read config and drop its cached
// folds, for an outfit added or edited by hand.
func runOutfitRefresh(loaded *config.Loaded) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	if _, err := acli.Configure(context.Background(), rpc.ConfigureRequest{Refresh: true}); err != nil {
		die("state outfit --refresh: %s", err)
	}
	fmt.Fprintln(os.Stderr, "outfits refreshed")
}

// runOutfitList prints the outfits the server has on disk.
func runOutfitList(loaded *config.Loaded) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	resp, err := acli.Outfits(context.Background(), "")
	if err != nil {
		die("state outfit --list: %s", err)
	}
	if len(resp.Names) == 0 {
		fmt.Fprintln(os.Stderr, "no outfits found")
		return
	}
	for _, n := range resp.Names {
		marker := ""
		if n == resp.Default {
			marker = " (default)"
		}
		fmt.Printf("%s%s\n", n, marker)
	}
}
