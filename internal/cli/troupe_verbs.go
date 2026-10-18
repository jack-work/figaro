package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"text/tabwriter"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
)

const (
	troupeSignedIn  = "signed in"
	troupeSignedOut = "signed out"
	troupeNoAuth    = "no [auth]"
)

type troupeRow struct {
	Name     string `json:"name"`
	URL      string `json:"url,omitempty"`
	SignIn   string `json:"sign_in,omitempty"`
	Selected bool   `json:"selected"`
	Error    string `json:"error,omitempty"`
}

func troupeRows(loaded *config.Loaded, store oauthStore) []troupeRow {
	names, err := boot.TroupeNames(loaded)
	if err != nil {
		die("%s", err)
	}
	var held []string
	if store != nil {
		held, _ = store.OAuthList()
	}
	selected := boot.SelectedTroupeName(loaded)
	rows := make([]troupeRow, 0, len(names))
	for _, n := range names {
		r := troupeRow{Name: n, Selected: n == selected}
		tr, err := boot.LoadTroupe(loaded, n)
		switch {
		case err != nil:
			r.Error = err.Error()
		case tr.Auth == nil:
			r.URL, r.SignIn = tr.URL, troupeNoAuth
		case slices.Contains(held, tr.HushCredential()):
			r.URL, r.SignIn = tr.URL, troupeSignedIn
		default:
			r.URL, r.SignIn = tr.URL, troupeSignedOut
		}
		rows = append(rows, r)
	}
	return rows
}

func runTroupeList(loaded *config.Loaded, jsonOut bool) {
	store, hushErr := cliOAuthStore(false)
	rows := troupeRows(loaded, store)
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(rows)
	} else if len(rows) == 0 {
		fmt.Fprintf(stderrw, "no troupes (%s/<name>.toml)\n", loaded.TroupesDir())
	} else {
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "\tTROUPE\tURL\tSIGN-IN")
		for _, r := range rows {
			mark := ""
			if r.Selected {
				mark = "●"
			}
			if r.Error != "" {
				fmt.Fprintf(w, "%s\t%s\t-\terror: %s\n", mark, r.Name, r.Error)
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", mark, r.Name, r.URL, r.SignIn)
		}
		_ = w.Flush()
	}
	if hushErr != nil {
		fmt.Fprintf(stderrw, "hush unavailable (%s): sign-in not shown\n", hushErr)
	}
}

func troupeForLogin(loaded *config.Loaded, name string) (boot.Troupe, error) {
	tr, err := boot.LoadTroupe(loaded, name)
	if err != nil {
		return tr, err
	}
	if tr.Auth == nil {
		return tr, fmt.Errorf("troupe %q has no [auth] to sign in with", name)
	}
	return tr, nil
}

func runTroupeLogin(loaded *config.Loaded, name string) {
	tr, err := troupeForLogin(loaded, name)
	if err != nil {
		die("%s", err)
	}
	next := "FIGARO_TROUPE=" + name + " figaro ls, or [cli] troupe = \"" + name + "\""
	if err := runOAuthLogin(name, tr.HushCredential(), tr.Auth.OAuthSettings, next); err != nil {
		die("%s", err)
	}
}

func runTroupeVerb(loaded *config.Loaded, args []string, jsonOut bool) error {
	verb, rest := args[0], args[1:]
	switch verb {
	case "ls":
		if len(rest) > 0 {
			dieUsage("troupe ls takes no arguments")
		}
		runTroupeList(loaded, jsonOut)
	case "login":
		if len(rest) != 1 {
			dieUsage("troupe login takes one troupe (figaro troupe ls)")
		}
		runTroupeLogin(loaded, rest[0])
	default:
		dieUsage("troupe: want login or ls, not %q", verb)
	}
	return nil
}
