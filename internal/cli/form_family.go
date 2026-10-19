package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// Dressing is required: `fig form new` never touches the default outfit; k=v
// positionals fold on top of -S.
func runFormNew(loaded *config.Loaded, outfits, set, del string, kvs []string, asJSON bool) {
	d := mustFormDress(outfits, set, del, kvs, "form new -O <names> [-S k=v] [k=v …]")
	createFormAndReport(loaded, "", d, asJSON)
}

// A fork nobody can name is refused by the store, so at least one of -O / k=v
// must be present.
func runFormFork(loaded *config.Loaded, parent, outfits, set, del string, kvs []string, asJSON bool) {
	if parent == "" {
		die("usage: form fork <@form-id> [-O <names>] [-S k=v] [k=v …]")
	}
	d := mustFormDress(outfits, set, del, kvs, "form fork <@form-id> [-O <names>] [-S k=v] [k=v …]")
	createFormAndReport(loaded, parent, d, asJSON)
}

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
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(map[string]any{
			"form_id": resp.FormID,
			"handle":  ariaHandle(resp.FormID),
			"version": resp.Version,
			"parent":  parent,
		})
		return
	}
	if parent == "" {
		fmt.Fprintf(stdout, "minted %s (version %d)\n", ariaName(resp.FormID), resp.Version)
	} else {
		fmt.Fprintf(stdout, "forked %s from %s (version %d)\n", ariaName(resp.FormID), ariaName(parent), resp.Version)
	}
	// A form you just minted is created for you, so the shell moves onto it.
	attendNew(ctx, acli, resp.FormID)
	fmt.Fprintf(stderrw, "attending %s (it binds with `fig bind %s`)\n", ariaName(resp.FormID), ariaName(resp.FormID))
}

// runFormLs scopes by attendance: a form shows its subtree, a figaro its
// nearest unbound ancestor's tree, else the top level.
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
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(forms)
		return
	}
	if len(forms) == 0 {
		fmt.Fprintln(stderrw, "no unbound forms; mint one with `fig form new -S name='…'`")
		return
	}
	short := namesOf(resp.Figaros)
	fmt.Fprintf(stdout, "%-22s %-22s %-20s %-8s %s\n", "FORM", "NAME", "TARGET", "AGE", "PARENT")
	for _, f := range forms {
		age := "-"
		if f.LastActive != 0 {
			age = relAge(f.LastActive)
		}
		fmt.Fprintf(stdout, "%-22s %-22s %-20s %-8s %s\n",
			nameIn(short, f.ID), truncRunes(dash(f.Name), 22), dash(nameIfSet(short, f.TargetAria)), age, dash(nameIfSet(short, f.Parent)))
	}
}

// "" means unscoped.
func attendedFormScope(ctx context.Context, acli *sdk.Angelus, byID map[string]rpc.FigaroInfoResponse) string {
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

// runBind: no positional means the attended form; "null" births the naked
// figaro. Never rebinds this shell.
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
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(map[string]any{"figaro_id": resp.FigaroID, "form_id": target})
		return
	}
	fmt.Fprintf(stdout, "bound %s from %s\n", ariaName(resp.FigaroID), ariaName(target))
	fmt.Fprintf(stderrw, "dormant until first use; attend it with `fig at %s`\n", ariaName(resp.FigaroID))
}
