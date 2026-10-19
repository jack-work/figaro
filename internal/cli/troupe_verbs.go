package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/figaro/sdk"
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

type troupeListing struct {
	Troupes []troupeRow                `json:"troupes"`
	Members *rpc.TroupeMembersResponse `json:"members,omitempty"`
}

func localMembers(loaded *config.Loaded) (*rpc.TroupeMembersResponse, error) {
	name, err := loaded.TroupeName()
	if err != nil {
		return nil, err
	}
	if peers, err := loaded.Peers(); err != nil || (len(peers) == 0 && name == "") {
		return nil, err
	}
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return nil, fmt.Errorf("daemon not running, so no peer versions: %w", err)
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return acli.TroupeMembers(ctx)
}

const quietAfter = 2 * time.Hour

func seen(p rpc.TroupeMember, now time.Time) string {
	if !p.Met || p.SeenAt == 0 {
		return "-"
	}
	since := now.Sub(time.UnixMilli(p.SeenAt))
	switch {
	case since > troupe.NodeTTL:
		return "lapsed, " + ago(since)
	case since > quietAfter:
		return "quiet, " + ago(since)
	}
	return ago(since)
}

func writeTroupeMembers(out io.Writer, m *rpc.TroupeMembersResponse) {
	ledger := m.Provider == config.ProviderFigaro
	fmt.Fprintf(out, "\nthis node %s, troupe membership version %d\n", m.Node, m.Version)
	if ledger && m.AdmittedBy != "" {
		fmt.Fprintf(out, "admitted by %s, owned by %s\n", m.AdmittedBy, m.Owner)
	}
	if k := m.Keys; k != nil {
		switch {
		case k.Error != "":
			fmt.Fprintf(out, "keys: %s\n", k.Error)
		case k.IntermediateExpires == 0:
			fmt.Fprintf(out, "troupe %s keys: node certificate until %s; no intermediate, so a signer renews it\n", k.Troupe, day(k.NodeExpires))
		default:
			fmt.Fprintf(out, "troupe %s keys: node certificate until %s, intermediate until %s\n", k.Troupe, day(k.NodeExpires), day(k.IntermediateExpires))
		}
	}
	if len(m.Peers) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		if ledger {
			fmt.Fprintln(w, "\tPEER\tENDPOINT\tOWNER\tADMITTED BY\tSIGNS\tSEEN\tVERSION\tDRIFT")
		} else {
			fmt.Fprintln(w, "\tPEER\tENDPOINT\tVERSION\tDRIFT")
		}
		for _, p := range m.Peers {
			cols := "\t" + p.Node + "\t" + p.Endpoint
			if ledger {
				signs := ""
				if p.Signs {
					signs = "yes"
				}
				cols += "\t" + p.Owner + "\t" + p.AdmittedBy + "\t" + signs + "\t" + seen(p, time.Now())
			}
			switch {
			case !p.Met:
				fmt.Fprintf(w, "%s\t-\tnot met\n", cols)
			case p.Drift:
				fmt.Fprintf(w, "%s\t%d\tdrift: ours is %d\n", cols, p.Version, m.Version)
			default:
				fmt.Fprintf(w, "%s\t%d\t\n", cols, p.Version)
			}
		}
		_ = w.Flush()
	}
	for _, g := range m.Gone {
		fmt.Fprintf(out, "out: %s (%s)\n", g.Node, g.By)
	}
}

func runTroupeList(loaded *config.Loaded, jsonOut bool) {
	store, hushErr := cliOAuthStore(false)
	rows := troupeRows(loaded, store)
	members, membersErr := localMembers(loaded)
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(troupeListing{Troupes: rows, Members: members})
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
	if members != nil && !jsonOut {
		writeTroupeMembers(stdout, members)
	}
	if membersErr != nil {
		fmt.Fprintf(stderrw, "peers: %s\n", membersErr)
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

type keyOpts struct {
	Root bool
}

func runTroupeVerb(loaded *config.Loaded, args []string, jsonOut bool, o joinOpts, iss keyOpts) error {
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
	case "join":
		if len(rest) != 1 {
			dieUsage("troupe join takes one troupe (troupes/<name>.toml names a member's door)")
		}
		runTroupeJoin(loaded, rest[0], o)
	case "leave":
		if len(rest) != 1 {
			dieUsage("troupe leave takes one troupe")
		}
		runTroupeLeave(loaded, rest[0], o)
	case "revoke":
		if len(rest) != 2 {
			dieUsage("troupe revoke takes a troupe and a node, and the root key on stdin")
		}
		runTroupeRevoke(loaded, rest[0], rest[1])
	case "policy":
		runTroupePolicy(loaded, rest, jsonOut, iss.Root)
	case "create", "intermediate":
		if len(rest) != 1 {
			dieUsage("troupe %s takes one troupe name, and the root key on stdin", verb)
		}
		if iss.Root {
			dieUsage("--root goes with troupe policy set")
		}
		runTroupeKeyVerb(verb, rest[0])
	default:
		dieUsage("troupe: want login, ls, join, leave, revoke, policy, create or intermediate, not %q", verb)
	}
	return nil
}

func day(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04Z")
}
