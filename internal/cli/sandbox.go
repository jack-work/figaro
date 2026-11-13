package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
)

var sandboxVerb = &cmdkit.Command{
	Name:  "sandbox",
	Group: "System",
	Short: "List the sandbox classes this daemon's nodes offer and the leases it holds, or reinstate a cordoned sandbox",
	Usage: "sandbox <ls [-j] | reinstate <sandbox>>",
	Long: `A figaro takes a sandbox from a node by naming its class at birth
(figaro new -S system.sandbox=<class>); its forks and successors share it,
and the kill of the last one resets the sandbox and returns it to the node.

  ls         each class with its node, workload and sandboxes, then every
             lease in the ledger: its sandbox, generation, state and holders
  reinstate  reset a cordoned sandbox with a new nonce and probe it again;
             it returns to its node only if both pass (owner only)`,
	Flags: []cmdkit.FlagDef{
		{Long: "json", Short: "j", IsBool: true, Description: "ls: machine-readable output"},
	},
	ArgsMin: 1,
	Run: func(ctx *cmdkit.RunContext) error {
		loaded, _ := ctx.Extra.(*config.Loaded)
		switch {
		case ctx.Args[0] == "ls" && len(ctx.Args) == 1:
			acli := mustConnectAngelus(loaded)
			defer acli.Close()
			resp, err := acli.Sandboxes(context.Background())
			if err != nil {
				return err
			}
			return printSandboxes(stdout, resp, ctx.BoolFlag("json"))
		case ctx.Args[0] == "reinstate" && len(ctx.Args) == 2:
			acli := mustConnectAngelus(loaded)
			defer acli.Close()
			if err := acli.ReinstateSandbox(context.Background(), ctx.Args[1]); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "sandbox %s reset, probed and returned to its node\n", ctx.Args[1])
			return nil
		}
		return fmt.Errorf("usage: sandbox <ls [-j] | reinstate <sandbox>>")
	},
}

func printSandboxes(w io.Writer, resp *rpc.SandboxListResponse, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	if len(resp.Classes) == 0 && len(resp.Leases) == 0 {
		fmt.Fprintln(w, "no sandbox class offered and no lease held")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tNODE\tWORKLOAD\tSANDBOXES\tEGRESS")
	for _, c := range resp.Classes {
		egress := strings.Join(c.Egress, ", ")
		if egress == "" {
			egress = "none"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Node, c.Workload, strings.Join(c.Sandboxes, ", "), egress)
	}
	tw.Flush()
	if len(resp.Leases) == 0 {
		fmt.Fprintln(w, "\nno lease held")
		return nil
	}
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SANDBOX\tCLASS\tGEN\tSTATE\tHOLDERS\tSINCE")
	for _, l := range resp.Leases {
		holders := make([]string, len(l.Holders))
		for i, h := range l.Holders {
			holders[i] = shortIDOf(h)
		}
		sandbox := l.Sandbox
		if sandbox == "" {
			sandbox = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", sandbox, l.Class, l.Generation, l.State, strings.Join(holders, ", "), l.Since)
	}
	return tw.Flush()
}

func shortIDOf(id string) string {
	if len(id) > rpc.ShortIDLen {
		return id[:rpc.ShortIDLen]
	}
	return id
}
