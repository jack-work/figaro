package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/valet"
)

var valetVerb = &cmdkit.Command{
	Name:  "valet",
	Group: "System",
	Short: "Grant figaros paths for the tools your valet runs as you",
	Usage: "valet grant <figaro|*> <path> ro|rw [--for 8h] | valet grants [-j] | valet revoke <figaro|*> [<path>] | valet serve [--peer <user>] [--door <path>] [--hide <path>]",
	Long: `The valet runs a figaro's tools as you, inside a fence that shows them
only the paths you granted that figaro. Grants live in your own table,
~/.local/state/figaro-valet/grants.toml (0600), which no tool can see.
To the daemon the valet is a box: [sandbox.boxes.<name>] exec names its
socket, and an outfit picks it with system.sandbox.

  grant    let <figaro> (eight or more characters of its id, or * for
           every figaro) see <path> read-only or read-write for --for
           (default 8h, at most --max-grant, default 7d). The path is
           resolved through symlinks; your home, anything holding it, and
           the valet's own state are refused: grant working trees
  grants   list the table, with when each grant runs out
  revoke   drop <figaro>'s grant of <path>, or every grant it holds
  serve    answer one call, from figaro-valet.socket (Accept=yes): admit
           the caller only if its uid is a --peer (default: yours), read
           the figaro from box.open, and serve that box from inside a
           fence: a user, mount and pid namespace whose root holds the
           system trees read-only, an empty home, the figaro's grants, a
           scratch directory under the valet's state, and --door. Your
           runtime directory, the nix daemon socket, the valet's state and
           every --hide path are absent. Refuses when unprivileged user
           namespaces are off`,
	Flags: []cmdkit.FlagDef{
		{Long: "for", Description: "grant: how long the grant lasts (default 8h; d and w suffixes work)"},
		{Long: "max-grant", Description: "grant, serve: the longest grant to accept or honour (default 7d)"},
		{Long: "state", Description: "the valet's state directory (default ~/.local/state/figaro-valet)"},
		{Long: "json", Short: "j", IsBool: true, Description: "grants: machine-readable output"},
		{Long: "peer", Repeats: true, Description: "serve: a user or uid that may call the valet; repeats"},
		{Long: "door", Description: "serve: the daemon's door socket for this box, bound into the fence"},
		{Long: "hide", Repeats: true, Description: "serve: a path kept from tools even inside a grant; repeats"},
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
		case "serve":
			v, err := valetFromFlags(state, ctx.Flag("peer"), ctx.Flag("max-grant"), ctx.Flag("door"), ctx.Flag("hide"))
			if err != nil {
				return err
			}
			return runValetServe(v)
		case "fence":
			return runValetFence()
		case "fenced":
			return runValetFenced()
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
		return fmt.Errorf("valet: want grant, grants, revoke or serve, not %q", ctx.Args[0])
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

func valetFromFlags(state, peers, maxRaw, door, hides string) (valet.Valet, error) {
	max, err := durationOr(maxRaw, valet.DefaultMaxGrant)
	if err != nil {
		return valet.Valet{}, err
	}
	v := valet.Valet{State: state, MaxGrant: max, Door: door, Log: stderrw}
	if v.Self, err = os.Executable(); err != nil {
		return v, err
	}
	if v.Home, err = os.UserHomeDir(); err != nil {
		return v, err
	}
	if v.State, err = filepath.Abs(state); err != nil {
		return v, err
	}
	v.Peers = []int{os.Getuid()}
	if peers != "" {
		v.Peers = nil
		for _, p := range strings.Split(peers, ",") {
			uid, err := lookupUID(p)
			if err != nil {
				return v, err
			}
			v.Peers = append(v.Peers, uid)
		}
	}
	v.Hide = []string{
		filepath.Join(v.Home, ".ssh"),
		filepath.Join(v.Home, ".gnupg"),
		boot.StateDir(),
		boot.CacheDir(),
		config.DefaultConfigDir(),
	}
	if hides != "" {
		v.Hide = append(v.Hide, strings.Split(hides, ",")...)
	}
	return v, nil
}

func lookupUID(name string) (int, error) {
	if uid, err := strconv.Atoi(name); err == nil {
		return uid, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("valet: --peer %s: %w", name, err)
	}
	return strconv.Atoi(u.Uid)
}
