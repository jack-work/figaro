package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type reapedGroup struct {
	Root    string   `json:"root"`
	Mantra  string   `json:"mantra,omitempty"`
	Nodes   []string `json:"nodes"`
	AtMS    int64    `json:"at"`
	PurgeMS int64    `json:"purge_at"`
}

func parseReapedForm(snap form.Snapshot) ([]reapedGroup, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items map[string]struct {
			Mantra  string   `json:"mantra"`
			Nodes   []string `json:"nodes"`
			At      int64    `json:"at"`
			PurgeAt int64    `json:"purge_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := make([]reapedGroup, 0, len(doc.Items))
	for root, it := range doc.Items {
		out = append(out, reapedGroup{Root: root, Mantra: it.Mantra, Nodes: it.Nodes, AtMS: it.At, PurgeMS: it.PurgeAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PurgeMS < out[j].PurgeMS })
	return out, nil
}

func readReaped(ctx context.Context, acli *sdk.Angelus) ([]reapedGroup, error) {
	if _, err := acli.Attach(ctx, rpc.ReapedFormID); err != nil {
		return nil, err
	}
	conn, err := sdk.DialAria(ariaAddr(rpc.ReapedFormID), nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := conn.Form(ctx)
	if err != nil {
		return nil, err
	}
	return parseReapedForm(resp.Snapshot)
}

func groupHolding(groups []reapedGroup, id string) (reapedGroup, bool) {
	for _, g := range groups {
		if g.Root == id {
			return g, true
		}
		for _, n := range g.Nodes {
			if n == id {
				return g, true
			}
		}
	}
	return reapedGroup{}, false
}

func runReaped(loaded *config.Loaded, args []string, keep, asJSON bool) error {
	verb := "ls"
	if len(args) > 0 {
		verb = args[0]
	}
	var target string
	if len(args) > 1 {
		target = args[1]
	}
	var err error
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if target != "" {
			if id, derr := doorID(ctx, acli, target); derr == nil {
				target = id
			}
		}
		var groups []reapedGroup
		if groups, err = readReaped(ctx, acli); err != nil {
			err = fmt.Errorf("reaped: %w", err)
			return nil
		}
		switch verb {
		case "ls", "list":
			err = printReaped(groups, asJSON)
		case "restore", "purge":
			if target == "" {
				dieUsage("usage: figaro reaped %s <id>", verb)
			}
			g, ok := groupHolding(groups, target)
			if !ok {
				err = fmt.Errorf("reaped: %s is not reaped (figaro reaped ls)", target)
				return nil
			}
			if verb == "purge" {
				if kerr := acli.Kill(ctx, g.Root, true); kerr != nil {
					err = fmt.Errorf("reaped purge: %w", kerr)
					return nil
				}
				fmt.Fprintf(stderrw, "purged %s and %d under it\n", ariaName(g.Root), len(g.Nodes)-1)
				return nil
			}
			err = restoreReaped(loaded, g, keep)
		default:
			dieUsage("usage: figaro reaped [ls | restore <id> [--keep] | purge <id>] [-j]")
		}
		return nil
	})
	return err
}

func restoreReaped(loaded *config.Loaded, g reapedGroup, keep bool) error {
	for _, id := range g.Nodes {
		drops := []string{store.SystemReapedKey}
		if keep && id == g.Root {
			drops = append(drops, store.SystemTTLKey)
		}
		mustCallSet(loaded, id, form.Build(form.Snapshot{}, nil, drops), 0)
	}
	note := "the keepalive holds it for a day; unset system.ttl or use --keep to keep it"
	if keep {
		note = "system.ttl removed from the root; it will not be reaped again"
	}
	fmt.Fprintf(stderrw, "restored %s and %d under it (%s)\n", ariaName(g.Root), len(g.Nodes)-1, note)
	return nil
}

func printReaped(groups []reapedGroup, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(groups)
	}
	if len(groups) == 0 {
		fmt.Fprintln(stderrw, "nothing is reaped")
		return nil
	}
	now := time.Now()
	fmt.Fprintf(stdout, "%-22s %-5s %-10s %-10s %s\n", "ARIA", "NODES", "REAPED", "PURGE IN", "MANTRA")
	for _, g := range groups {
		purge := "due"
		if left := time.UnixMilli(g.PurgeMS).Sub(now); left > 0 {
			purge = shortDuration(left)
		}
		fmt.Fprintf(stdout, "%-22s %-5d %-10s %-10s %s\n",
			truncRunes(ariaName(g.Root), 22), len(g.Nodes), relAge(g.AtMS), purge, truncRunes(g.Mantra, 48))
	}
	return nil
}
