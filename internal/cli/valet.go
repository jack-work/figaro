package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/sandbox/valet"
)

var valetVerb = &cmdkit.Command{
	Name:  "valet",
	Group: "System",
	Short: "Grant figaros paths for the tools your valet runs as you",
	Usage: "valet grant <figaro|*> <path> ro|rw [--for 8h] | valet grants [-j] | valet revoke <figaro|*> [<path>]",
	Long: `The valet runs a figaro's tools as you, inside a fence that shows them
only the paths you granted that figaro. Grants live in your own table,
~/.local/state/figaro-valet/grants.toml (0600), which no tool can see.

  grant    let <figaro> (eight or more characters of its id, or * for
           every figaro) see <path> read-only or read-write for --for
           (default 8h, at most --max-grant, default 7d). The path is
           resolved through symlinks; your home, anything holding it, and
           the valet's own state are refused: grant working trees
  grants   list the table, with when each grant runs out
  revoke   drop <figaro>'s grant of <path>, or every grant it holds`,
	Flags: []cmdkit.FlagDef{
		{Long: "for", Description: "grant: how long the grant lasts (default 8h; d and w suffixes work)"},
		{Long: "max-grant", Description: "grant: the longest grant to accept (default 7d)"},
		{Long: "state", Description: "the valet's state directory (default ~/.local/state/figaro-valet)"},
		{Long: "json", Short: "j", IsBool: true, Description: "grants: machine-readable output"},
	},
	ArgsMin: 1,
	Run: func(ctx *cmdkit.RunContext) error {
		state := ctx.Flag("state")
		if state == "" {
			state = valet.StateDir()
		}
		switch ctx.Args[0] {
		case "grant":
			if len(ctx.Args) != 4 {
				return fmt.Errorf("valet grant <figaro|*> <path> ro|rw")
			}
			return runValetGrant(state, ctx.Args[1], ctx.Args[2], ctx.Args[3], ctx.Flag("for"), ctx.Flag("max-grant"))
		case "grants":
			return runValetGrants(state, ctx.BoolFlag("json"))
		case "revoke":
			if len(ctx.Args) < 2 || len(ctx.Args) > 3 {
				return fmt.Errorf("valet revoke <figaro|*> [<path>]")
			}
			path := ""
			if len(ctx.Args) == 3 {
				p, err := valet.Canonical(ctx.Args[2])
				if err != nil {
					p = ctx.Args[2]
				}
				path = p
			}
			return runValetRevoke(state, ctx.Args[1], path)
		}
		return fmt.Errorf("valet: want grant, grants or revoke, not %q", ctx.Args[0])
	},
}

func runValetStandalone(progName string, args []string) int {
	r := cmdkit.NewRouter(progName)
	r.Register(valetVerb)
	return r.Run(args)
}

func durationOr(raw string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	return valet.ParseDuration(raw)
}

func runValetGrant(state, figaro, path, mode, forRaw, maxRaw string) error {
	span, err := durationOr(forRaw, 8*time.Hour)
	if err != nil {
		return err
	}
	max, err := durationOr(maxRaw, valet.DefaultMaxGrant)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	guarded, err := valet.Canonical(state)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if h, err := valet.Canonical(home); err == nil {
		home = h
	}
	now := time.Now()
	g, err := valet.Request{Figaro: figaro, Path: path, Mode: mode, For: span, Max: max, Home: home, Guarded: []string{guarded}}.Grant(now)
	if err != nil {
		return fmt.Errorf("valet grant: %w", err)
	}
	tab, err := valet.Load(state)
	if err != nil {
		return err
	}
	tab.Prune(now, max)
	tab.Add(g)
	if err := tab.Save(state); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "granted %s %s %s until %s\n", g.Figaro, g.Path, g.Mode, g.Expires.Local().Format(time.DateTime))
	return nil
}

func runValetGrants(state string, asJSON bool) error {
	tab, err := valet.Load(state)
	if err != nil {
		return err
	}
	if asJSON {
		grants := tab.Grants
		if grants == nil {
			grants = []valet.Grant{}
		}
		return json.NewEncoder(stdout).Encode(grants)
	}
	if len(tab.Grants) == 0 {
		fmt.Fprintln(stdout, "no grants: every figaro gets an empty scratch directory")
		return nil
	}
	now := time.Now()
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FIGARO\tPATH\tMODE\tLEFT")
	for _, g := range tab.Grants {
		left := "expired"
		if d := g.Expires.Sub(now); d > 0 {
			left = shortDuration(d)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", g.Figaro, g.Path, g.Mode, left)
	}
	return w.Flush()
}

func runValetRevoke(state, figaro, path string) error {
	tab, err := valet.Load(state)
	if err != nil {
		return err
	}
	n := tab.Revoke(figaro, path)
	if n == 0 {
		return fmt.Errorf("valet revoke: %s holds no such grant", figaro)
	}
	if err := tab.Save(state); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "revoked %d grant(s)\n", n)
	return nil
}
