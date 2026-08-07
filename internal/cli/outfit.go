// Package cli — `figaro outfit` command.
//
// Applies named outfits additively to the current aria's chalkboard. The
// outfit files are resolved by the angelus (it owns the configDir); the CLI
// only forwards the names.
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
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// completeOutfits completes the `outfit` command: available outfit names for
// the positional slot (sourced from the config, so it works with no aria
// attached), or aria ids after --id. A comma-separated list completes its last
// segment, so `figaro outfit a,<TAB>` offers the rest.
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
	for _, n := range splitOutfitNames(prefix) {
		chosen[n] = true
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !chosen[n] {
			out = append(out, prefix+n)
		}
	}
	return out
}

// splitOutfitNames parses the comma-separated form. Comma is the separator, so
// an outfit whose file name contains one cannot be named this way.
func splitOutfitNames(arg string) []string {
	var out []string
	for _, part := range strings.Split(arg, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// runOutfit calls figaro.outfit on the targeted aria.
func runOutfit(loaded *config.Loaded, ariaID, arg string) {
	names := splitOutfitNames(arg)
	if len(names) == 0 {
		die("usage: figaro outfit [--id <id>] <name>[,<name>...]")
	}
	label := strings.Join(names, ",")

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

	resp, err := fcli.Outfit(ctx, names)
	if err != nil {
		dieOutfitFailure(label, err)
	}

	if len(resp.Set) == 0 {
		fmt.Fprintf(os.Stderr, "outfit %q: no changes (chalkboard already matches)\n", label)
		return
	}
	fmt.Fprintf(os.Stderr, "outfit %q applied (%d keys):\n", label, len(resp.Set))
	for _, k := range resp.Set {
		fmt.Fprintf(os.Stderr, "  %s\n", k)
	}
}

// dieOutfitFailure reports a failed apply.
func dieOutfitFailure(label string, err error) {
	dieWithClosure(err, "outfit %q: %s", label, err)
}

// dieWithClosure reports err, drawing the outfit layer closure when the server
// sent one. The shape is the explanation: every outfit that was found is green
// and every one that was not is red, so a broken reference several layers down
// is located rather than merely named. Without a closure it is an ordinary die.
func dieWithClosure(err error, format string, args ...any) {
	closure := outfitClosureFrom(err)
	if closure == nil {
		die(format, args...)
	}
	fmt.Fprintf(os.Stderr, "error: "+format+"\n\n", args...)
	fmt.Fprint(os.Stderr, renderOutfitClosure(closure))
	fmt.Fprintln(os.Stderr)
	exitNow(1)
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
