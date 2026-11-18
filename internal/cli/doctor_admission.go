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
	return printAdmission(stdout, resp.Drift, resp.Switches, jsonOut)
}

func printAdmission(w io.Writer, drift []rpc.FanoutDrift, switches []rpc.EnforcementSwitch, jsonOut bool) error {
	if drift == nil {
		drift = []rpc.FanoutDrift{}
	}
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"count": len(drift), "drift": drift, "switches": switches})
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
		return nil
	}
	fmt.Fprintf(w, "admission: %d node(s) where the cache and the tree disagree (the next sweep rebuilds the cache)\n", len(drift))
	for _, d := range drift {
		fmt.Fprintf(w, "  %s\n    cache: %s\n    tree:  %s\n", d.ID, d.Cached, d.Tree)
	}
	return nil
}
