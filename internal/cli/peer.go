package cli

// `figaro peer` -- the federated node list.
//
// THE CLI STAYS DUMB. It does not dial a peer, hold a credential, or resolve
// a URL: it asks the LOCAL daemon, which owns all three. Everything remote
// arrives as a richer answer to a local call, and that is what keeps every
// other verb working unchanged across a federation.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

func runPeer(loaded *config.Loaded, args []string, jsonOut bool) error {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var req rpc.PeerRequest
	switch {
	case len(args) == 0 || args[0] == "ls":
	case args[0] == "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: figaro peer add <name> <url> [--token-file <path>]")
		}
		req.Peer = &rpc.PeerSpec{Name: args[1], URL: args[2]}
		for i := 3; i+1 < len(args)+1; i++ {
			if args[i] == "--token-file" && i+1 < len(args) {
				req.Peer.TokenFile = args[i+1]
			}
		}
	case args[0] == "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: figaro peer rm <name>")
		}
		req.Remove = args[1]
	default:
		return fmt.Errorf("usage: figaro peer [ls] | peer add <name> <url> [--token-file <p>] | peer rm <name>")
	}

	resp, err := acli.Peers(ctx, req)
	if err != nil {
		return err
	}
	if jsonOut {
		b, _ := json.Marshal(resp)
		fmt.Fprintln(stdout, string(b))
		return nil
	}
	if len(resp.Peers) == 0 {
		fmt.Fprintln(stderrw, "no peers. `figaro peer add <name> <url>` to federate with one.")
		return nil
	}
	for _, p := range resp.Peers {
		state := resp.Reachable[p.Name]
		if state == "" {
			state = "?"
		}
		fmt.Fprintf(stdout, "%-12s %-38s %s\n", p.Name, p.URL, state)
	}
	return nil
}
