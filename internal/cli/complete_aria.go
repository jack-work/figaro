package cli

import (
	"context"
	"github.com/jack-work/figaro/sdk"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/cmdkit"
)

// softFetchAriaIDs best-effort fetches the list of known aria ids
// (live + dormant) via the angelus RPC surface. Returns nil on any
// failure: completion must never autostart the daemon, prompt the
// user, or block long. CLI stays backend-agnostic: the daemon is
// the source of truth, the CLI never touches the on-disk aria dir.
func softFetchAriaIDs() []string {
	ep := transport.UnixEndpoint(angelusSocketPath())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(ep)
	if err != nil {
		return nil
	}
	defer acli.Close()
	resp, err := acli.ListIDs(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(resp.Figaros))
	for _, f := range resp.Figaros {
		if f.ID != "" {
			out = append(out, f.ID)
		}
	}
	sort.Strings(out)
	return out
}

// completeAriaIDsAfterFlag returns aria ids when the previous token
// is --id (i.e. the cursor is about to type the flag's value). For
// every other position it falls through to inner (which may be nil).
func completeAriaIDsAfterFlag(inner func(*cmdkit.CompleteContext) []string) func(*cmdkit.CompleteContext) []string {
	return func(c *cmdkit.CompleteContext) []string {
		if c != nil && len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
			return softFetchAriaIDs()
		}
		if inner != nil {
			return inner(c)
		}
		return nil
	}
}

// completeAriaIDsPositionalOrFlag combines two behaviors used by
// commands like `kill` and `status` that accept the aria id either
// as a positional or after --id:
func completeAriaIDsPositionalOrFlag(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return softFetchAriaIDs()
	}
	// First positional slot: no args, or only flags before this point.
	// Conservative: only fire when Args is empty, to avoid suggesting
	// ids in the middle of a flag value the router doesn't model.
	if len(c.Args) == 0 {
		return softFetchAriaIDs()
	}
	return nil
}
