package cli

import (
	"context"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/cmdkit"
)

// softFetchAriaIDs returns nil on any failure: completion must never autostart
// the daemon, prompt, or block long.
func softFetchAriaIDs() []string {
	ep := transport.UnixEndpoint(boot.AngelusSocketPath())
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

func completeAriaIDsAfterFlag(inner func(*cmdkit.CompleteContext) []string) func(*cmdkit.CompleteContext) []string {
	return func(c *cmdkit.CompleteContext) []string {
		if c != nil && len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
			return ariaCandidates(c)
		}
		if inner != nil {
			return inner(c)
		}
		return nil
	}
}

func completeAriaIDsPositionalOrFlag(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return ariaCandidates(c)
	}
	// The first positional slot is empty when only flags precede the cursor; a
	// value-taking flag's value reads as a positional, erring toward offering
	// nothing.
	for _, a := range c.Args {
		if !strings.HasPrefix(a, "-") {
			return nil
		}
	}
	return ariaCandidates(c)
}
