package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/api/rpc"
)

// The `fig form new|fork|ls|rm` family: unbound forms as first-class
// primitives. Semantics: plans/forms-and-roles-v2.md; the daemon half is
// form.create + the hub's agentless read/write paths.

// runFormNew mints an unbound form. Dressing is REQUIRED: `fig form new`
// never touches the default outfit, and extra k=v positionals fold on top of
// -S, later terms winning.
func runFormNew(loaded *config.Loaded, outfits, set, del string, kvs []string, asJSON bool) {
	d := mustFormDress(outfits, set, del, kvs, "form new -O <names> [-S k=v] [k=v …]")
	createFormAndReport(loaded, "", d, asJSON)
}

// runFormFork duplicates a form's state into a fresh @id. The patch is
// required by the one-birth-verb law (a fork nobody can name is refused
// by the store), so at least one of -O / k=v must be present.
func runFormFork(loaded *config.Loaded, parent, outfits, set, del string, kvs []string, asJSON bool) {
	if parent == "" {
		die("usage: form fork <@form-id> [-O <names>] [-S k=v] [k=v …]")
	}
	d := mustFormDress(outfits, set, del, kvs, "form fork <@form-id> [-O <names>] [-S k=v] [k=v …]")
	createFormAndReport(loaded, parent, d, asJSON)
}

// mustFormDress assembles the birth dressing: outfit NAMES from -O, keys from
// -S and from the bare k=v positionals (which are the same grammar, so they
// simply join the -S terms), removals from -D.
func mustFormDress(outfits, set, del string, kvs []string, usage string) dressing {
	terms := make([]string, 0, len(kvs)+1)
	if strings.TrimSpace(set) != "" {
		terms = append(terms, set)
	}
	terms = append(terms, kvs...)
	d, err := parseDress(outfits, strings.Join(terms, ","), del)
	if err != nil {
		die("%s", err)
	}
	if d.IsEmpty() {
		die("a form is born of its patch: give -O <names> and/or -S k=v terms\nusage: %s", usage)
	}
	return d
}

func createFormAndReport(loaded *config.Loaded, parent string, d dressing, asJSON bool) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := acli.FormCreate(ctx, parent, d.names, d.patch)
	if err != nil {
		die("form: %s", err)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(map[string]any{
			"form_id": resp.FormID,
			"version": resp.Version,
			"parent":  parent,
		})
		return
	}
	if parent == "" {
		fmt.Printf("minted %s (version %d)\n", resp.FormID, resp.Version)
	} else {
		fmt.Printf("forked %s from %s (version %d)\n", resp.FormID, parent, resp.Version)
	}
	fmt.Fprintf(os.Stderr, "attend it with `fig at %s`; it binds with `fig bind %s`\n", resp.FormID, resp.FormID)
}

// runFormLs lists unbound forms: the form rows of the global listing,
// scoped by attendance per the brief, attending a form shows its
// subtree, attending a figaro shows its nearest unbound ancestor's tree,
// otherwise the top level.
func runFormLs(loaded *config.Loaded, asJSON bool) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := acli.ListGlobal(ctx)
	if err != nil {
		die("form ls: %s", err)
	}

	byID := map[string]rpc.FigaroInfoResponse{}
	for _, f := range resp.Figaros {
		byID[f.ID] = f
	}
	var forms []rpc.FigaroInfoResponse
	for _, f := range resp.Figaros {
		if f.Kind == "form" {
			forms = append(forms, f)
		}
	}

	// Scope: the attended node decides which part of the genealogy shows.
	if scope := attendedFormScope(ctx, acli, byID); scope != "" {
		var kept []rpc.FigaroInfoResponse
		for _, f := range forms {
			if formDescendsFrom(byID, f.ID, scope) {
				kept = append(kept, f)
			}
		}
		forms = kept
	}

	sort.SliceStable(forms, func(i, j int) bool { return forms[i].LastActive > forms[j].LastActive })

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(forms)
		return
	}
	if len(forms) == 0 {
		fmt.Fprintln(os.Stderr, "no unbound forms; mint one with `fig form new -S name='…'`")
		return
	}
	fmt.Printf("%-14s %-22s %-10s %-8s %s\n", "FORM", "NAME", "TARGET", "AGE", "PARENT")
	for _, f := range forms {
		age := "-"
		if f.LastActive != 0 {
			age = relAge(f.LastActive)
		}
		fmt.Printf("%-14s %-22s %-10s %-8s %s\n",
			f.ID, truncRunes(dash(f.Name), 22), dash(f.TargetAria), age, dash(f.Parent))
	}
}

// attendedFormScope maps this shell's attendance to a form-tree scope: the
// attended form itself, or an attended figaro's nearest unbound ancestor.
// "" means unscoped (top level, attending null, or nothing resolvable).
func attendedFormScope(ctx context.Context, acli *angelus.Client, byID map[string]rpc.FigaroInfoResponse) string {
	r, err := resolveBinding(ctx, acli, shellPID)
	if err != nil || r == nil || !r.Found {
		return ""
	}
	node, ok := byID[r.FigaroID]
	if !ok {
		return ""
	}
	if node.Kind == "form" {
		return node.ID
	}
	// A figaro: climb to the nearest unbound ancestor.
	for cur := node; ; {
		parent, ok := byID[cur.Parent]
		if !ok {
			return ""
		}
		if parent.Kind == "form" {
			return parent.ID
		}
		cur = parent
	}
}

// formDescendsFrom reports whether id sits in scope's subtree (inclusive).
func formDescendsFrom(byID map[string]rpc.FigaroInfoResponse, id, scope string) bool {
	for cur := id; cur != ""; {
		if cur == scope {
			return true
		}
		n, ok := byID[cur]
		if !ok {
			return false
		}
		cur = n.Parent
	}
	return false
}

// runBind births a figaro from an unbound form. No positional: the
// attended form. "null": the naked figaro (fails its first TURN unless
// -O or later patches supply a provider: minting is not the gate).
// Never rebinds this shell: the printed id is attended by hand.
func runBind(loaded *config.Loaded, target, outfits, set, del string, asJSON bool) {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if target == "" {
		r, err := resolveBinding(ctx, acli, shellPID)
		if err != nil || r == nil || !r.Found {
			die("bind: nothing attended; name the form: fig bind <@form-id|null>")
		}
		if !strings.HasPrefix(r.FigaroID, "@") {
			die("bind: attending %s, which is a figaro, not a form.\n  fig fork          branch the attended figaro\n  fig bind <@form>  birth a figaro from a form", r.FigaroID)
		}
		target = r.FigaroID
	}

	d, err := parseDress(outfits, set, del)
	if err != nil {
		die("bind: %s", err)
	}
	resp, err := acli.FormBind(ctx, target, d.names, d.patch)
	if err != nil {
		die("bind: %s", err)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(map[string]any{"figaro_id": resp.FigaroID, "form_id": target})
		return
	}
	fmt.Printf("bound %s from %s\n", resp.FigaroID, target)
	fmt.Fprintf(os.Stderr, "dormant until first use; attend it with `fig at %s`\n", resp.FigaroID)
}
