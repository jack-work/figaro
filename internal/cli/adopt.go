package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

func printParked(verb string, p *rpc.Proposal) {
	fmt.Fprintf(stderrw, "%s: nothing moved yet: the move waits on %s as proposal %s\n"+
		"  its holder accepts with: figaro adopt accept %s %s\n"+
		"  it lapses at %s, or if the daemon restarts\n",
		verb, ariaName(p.Parent), p.ID, p.Parent, p.ID, time.UnixMilli(p.ExpiresMS).Format(time.DateTime))
}

func runAdopt(loaded *config.Loaded, args []string, asJSON bool) {
	verb, parent, id := "ls", "", ""
	switch {
	case len(args) == 1:
		parent = args[0]
	case len(args) == 3 && (args[0] == "accept" || args[0] == "refuse"):
		verb, parent, id = args[0], args[1], args[2]
	default:
		dieUsage("adopt: want `adopt <parent>`, `adopt accept <parent> <id>` or `adopt refuse <parent> <id>`")
	}
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		switch verb {
		case "accept":
			if err := acli.Accept(ctx, parent, id); err != nil {
				die("adopt accept: %s", err)
			}
			fmt.Fprintf(stderrw, "accepted %s: the move under %s is made\n", id, ariaName(parent))
			return nil
		case "refuse":
			if err := acli.Refuse(ctx, parent, id); err != nil {
				die("adopt refuse: %s", err)
			}
			fmt.Fprintf(stderrw, "refused %s: it is gone\n", id)
			return nil
		}
		resp, err := acli.Parked(ctx, parent)
		if err != nil {
			die("adopt: %s", err)
		}
		if asJSON {
			return json.NewEncoder(stdout).Encode(resp)
		}
		if len(resp.Proposals) == 0 {
			fmt.Fprintf(stderrw, "nothing waits on %s\n", ariaName(parent))
			return nil
		}
		for _, p := range resp.Proposals {
			var moves []string
			for _, m := range p.Moves {
				moves = append(moves, fmt.Sprintf("%s %s>%s", m.Node, sideOf(m.From, m.FromWithheld), sideOf(m.To, m.ToWithheld)))
			}
			if p.Withheld > 0 {
				moves = append(moves, fmt.Sprintf("%d more you cannot read", p.Withheld))
			}
			fmt.Fprintf(stdout, "%s  %s %s by %s, until %s\n    %s\n", p.ID, p.Op, orDash(p.Node), p.By,
				time.UnixMilli(p.ExpiresMS).Format(time.DateTime), strings.Join(moves, ", "))
		}
		return nil
	})
}

func sideOf(id string, withheld bool) string {
	if withheld {
		return "…"
	}
	return orDash(id)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
