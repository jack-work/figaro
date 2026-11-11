package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

func runScopes(loaded *config.Loaded, idFlag string, args []string, jsonOut bool) {
	var nameArg string
	if len(args) > 0 {
		nameArg = args[0]
	}
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, f, err := statusRow(ctx, acli, idFlag, nameArg)
		if err != nil {
			return err
		}
		g, err := acli.Grants(ctx, f.ID)
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(stdout).Encode(g)
		}
		ceiling := "none: every figaro verb, no other"
		if g.CeilingSet {
			ceiling = grantList(g.Ceiling)
		}
		rows := [][2]string{{"effective", grantList(g.Effective)}, {"ceiling", ceiling}}
		for _, l := range g.Chain {
			own := "not set: narrows nothing"
			switch {
			case l.Unreadable:
				own = "unreadable: holds nothing"
			case l.Set:
				own = grantList(l.Scopes)
				if l.Scopes == nil {
					own = grantList(l.Grants)
				}
			}
			rows = append(rows, [2]string{ariaName(l.FigaroID), own})
		}
		width := 0
		for _, r := range rows {
			width = max(width, len(r[0]))
		}
		for _, r := range rows {
			fmt.Fprintf(stdout, "%-*s  %s\n", width, r[0], r[1])
		}
		return nil
	})
}

func grantList(gs []string) string {
	if len(gs) == 0 {
		return "nothing"
	}
	return strings.Join(gs, ", ")
}
