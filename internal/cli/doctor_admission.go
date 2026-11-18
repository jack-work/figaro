package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

func runDoctorAdmission(loaded *config.Loaded, jsonOut bool) error {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	resp, err := acli.Admission(context.Background())
	if err != nil {
		return err
	}
	return printAdmission(stdout, resp.Drift, resp.Over, resp.Switches, jsonOut)
}

func printAdmission(w io.Writer, drift []rpc.FanoutDrift, over []rpc.FanoutOver, switches []rpc.EnforcementSwitch, jsonOut bool) error {
	if drift == nil {
		drift = []rpc.FanoutDrift{}
	}
	if over == nil {
		over = []rpc.FanoutOver{}
	}
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"count": len(drift), "drift": drift, "over": over, "switches": switches})
	}
	for _, s := range switches {
		state := "off: logged, not enforced"
		if s.On {
			state = "on"
		}
		fmt.Fprintf(w, "%s: %s; would have refused %d since the daemon started\n", s.Name, state, s.WouldRefuse)
	}
	if len(drift) == 0 {
		fmt.Fprintln(w, "admission: the cached fan-out counts match the tree")
	} else {
		fmt.Fprintf(w, "admission: %d node(s) where the cache and the tree disagree (the next sweep rebuilds the cache)\n", len(drift))
		for _, d := range drift {
			fmt.Fprintf(w, "  %s\n    cache: %s\n    tree:  %s\n", d.ID, d.Cached, d.Tree)
		}
	}
	if len(over) > 0 {
		fmt.Fprintf(w, "admission: %d figaro(s) over budget, refused every fork until under\n", len(over))
		for _, o := range over {
			fmt.Fprintf(w, "  %s %s\n", o.ID, o.Over)
		}
	}
	return nil
}
