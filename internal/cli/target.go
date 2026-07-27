package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// extractIDFlag scans a PassRaw arg list for `--id <value>` or
// `--id=<value>`, returning the id and the args with the flag removed.
//
// Only the prefix *before* `--` is searched; anything after `--` is the
// user's prompt and must not be molested.
//
// On `--id` with no value, or an invalid id, returns an error. An
// absent flag is not an error: id is "" and rest == args.
//
// Nothing before `--` is discarded: a token that is neither `--id` nor
// one of the caller's own flags is an error. Swallowing argv is how
// `figaro plain --nope -- hi` came to run with the flag on the floor.
func extractIDFlag(args []string) (id string, rest []string, err error) {
	return extractIDFlagAllowing(args, nil)
}

// extractIDFlagAllowing is extractIDFlag for verbs that parse a few
// flags of their own out of rest (x's -n/-y): those are passed through
// untouched, everything else before `--` is still an error.
func extractIDFlagAllowing(args []string, passthrough []string) (id string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			return id, rest, nil
		}
		switch {
		case a == "--id":
			if i+1 >= len(args) || args[i+1] == "--" {
				return "", nil, fmt.Errorf("--id requires a value")
			}
			if id != "" {
				return "", nil, fmt.Errorf("--id given more than once")
			}
			id = args[i+1]
			if err := rpc.ValidateAriaID(id); err != nil {
				return "", nil, fmt.Errorf("--id %q: %w", id, err)
			}
			i += 2
			continue
		case strings.HasPrefix(a, "--id="):
			if id != "" {
				return "", nil, fmt.Errorf("--id given more than once")
			}
			id = strings.TrimPrefix(a, "--id=")
			if id == "" {
				return "", nil, fmt.Errorf("--id requires a value")
			}
			if err := rpc.ValidateAriaID(id); err != nil {
				return "", nil, fmt.Errorf("--id %q: %w", id, err)
			}
			i++
			continue
		case slices.Contains(passthrough, a):
			rest = append(rest, a)
			i++
			continue
		}
		// Anything else before `--` is unconsumed argv. Never drop it.
		if strings.HasPrefix(a, "-") {
			return "", nil, fmt.Errorf("unknown flag %q (flags go before `--`; everything after `--` is the prompt)", a)
		}
		return "", nil, fmt.Errorf("the prompt must follow `--` (got bare argument %q)", a)
	}
	return id, rest, nil
}

// resolveTargetEndpoint resolves both id and endpoint. Used by verbs
// that talk to the figaro directly (send, plain, x, set, state...).
// Aria ids are system-minted, so a missing explicitID is always an
// error — there is no create-by-name. autoCreate is retained for call
// compatibility but no longer creates.
func resolveTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *angelus.Client, explicitID string, autoCreate bool) (string, transport.Endpoint, error) {
	_ = loaded
	_ = autoCreate
	if explicitID == "" {
		r, err := resolveBinding(ctx, acli, os.Getppid())
		if err != nil {
			return "", transport.Endpoint{}, fmt.Errorf("resolve: %w", err)
		}
		if !r.Found {
			if bindingDisabled() {
				return "", transport.Endpoint{}, fmt.Errorf("no aria specified (pass --id <id>; binding disabled in this shell)")
			}
			return "", transport.Endpoint{}, fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
		}
		return r.FigaroID, transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}, nil
	}

	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, explicitID)
	cancel()
	if err == nil {
		ep := transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
		if err := waitForSocket(ep.Address, 3*time.Second); err != nil {
			return "", transport.Endpoint{}, err
		}
		return explicitID, ep, nil
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not in tree") {
		return "", transport.Endpoint{}, fmt.Errorf("no such aria: %s", explicitID)
	}
	return "", transport.Endpoint{}, fmt.Errorf("attach %q: %w", explicitID, err)
}
