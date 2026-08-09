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
	loaded, _ := c.Extra.(*config.Loaded)
	if loaded == nil {
		return nil
	}
	names := loaded.ListOutfits()
	prefix := ""
	if i := strings.LastIndex(c.Current, ","); i >= 0 {
		prefix = c.Current[:i+1]
	}
	if prefix == "" {
		return names
	}
	chosen := map[string]bool{}
	if spec, err := outfit.ParseSpec(prefix); err == nil {
		for _, t := range spec {
			chosen[t.Name] = true
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

// mustParseSpec parses the CLI syntax or explains why it cannot, without
// opening a socket.
func mustParseSpec(arg, usage string) outfit.Spec {
	spec, err := outfit.ParseSpec(arg)
	if err != nil {
		die("%s", err)
	}
	if spec.IsEmpty() {
		die("usage: %s", usage)
	}
	return spec
}

// runOutfit calls figaro.outfit on the targeted aria.
func runOutfit(loaded *config.Loaded, ariaID, arg string) {
	spec := mustParseSpec(arg, "figaro state outfit [--id <id>] <spec>")
	label := shortSpec(spec)

	ctx := context.Background()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false, nil)
	if err != nil {
		die("%s", err)
	}

	fcli, err := figaro.DialClient(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address}, nil)
	if err != nil {
		die("dial aria: %s", err)
	}
	defer fcli.Close()

	resp, err := fcli.Outfit(ctx, spec)
	if err != nil {
		dieOutfitFailure(label, err)
	}

	if len(resp.Set) == 0 {
		fmt.Fprintf(os.Stderr, "outfit %s: no changes (chalkboard already matches)\n", label)
		return
	}
	fmt.Fprintf(os.Stderr, "outfit %s applied (%d keys):\n", label, len(resp.Set))
	for _, k := range resp.Set {
		fmt.Fprintf(os.Stderr, "  %s\n", k)
	}
}

// shortSpec renders a spec for a human-facing line. A literal can be
// kilobytes; a notice that reprints it is not a notice.
func shortSpec(spec outfit.Spec) string {
	text := spec.String()
	if len(text) <= 72 {
		return text
	}
	return text[:69] + "..."
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
// It resolves against the config dir directly rather than asking an aria, so it
// works with nothing bound and no daemon running — which is what inspecting a
// composition wants. Exit status follows the closure: 0 when every layer was
// found, 1 when the picture has red in it, so it can gate a script.
func runOutfitTree(loaded *config.Loaded, arg string) {
	if strings.TrimSpace(arg) == "" {
		arg = loaded.Config.DefaultOutfit
	}
	if strings.TrimSpace(arg) == "" {
		die("state outfit --tree: name an outfit, or set default_outfit in %s", loaded.ConfigPath)
	}
	spec := mustParseSpec(arg, "figaro state outfit --tree <spec>")
	closure := figaro.OutfitClosureWire(outfit.New(loaded.ConfigDir).ResolveSpec(spec))
	fmt.Print(renderOutfitClosure(closure))
	if broken := outfitClosureBroken(closure); len(broken) > 0 {
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
