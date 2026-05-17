package cli

import (
	"context"
	"fmt"
	"os"
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
func extractIDFlag(args []string) (id string, rest []string, err error) {
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
		}
		rest = append(rest, a)
		i++
	}
	return id, rest, nil
}

// resolveTargetAria returns the aria id to operate on, honoring (in
// order): explicit id > pid binding > error. It does NOT auto-create
// anything; callers that want create-on-miss should follow up with
// CreateWithID.
//
// Connects to the angelus internally when explicitID is empty.
func resolveTargetAria(ctx context.Context, acli *angelus.Client, explicitID string) (string, error) {
	if explicitID != "" {
		return explicitID, nil
	}
	r, err := acli.Resolve(ctx, os.Getppid())
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	if !r.Found {
		return "", fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
	}
	return r.FigaroID, nil
}

// resolveTargetEndpoint resolves both id and endpoint. Used by verbs
// that talk to the figaro directly (send, plain, x, set, state...).
// When explicitID names a missing aria and autoCreate is true, creates
// it; otherwise returns a "not found"-shaped error.
func resolveTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *angelus.Client, explicitID string, autoCreate bool) (string, transport.Endpoint, error) {
	if explicitID == "" {
		r, err := acli.Resolve(ctx, os.Getppid())
		if err != nil {
			return "", transport.Endpoint{}, fmt.Errorf("resolve: %w", err)
		}
		if !r.Found {
			return "", transport.Endpoint{}, fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
		}
		return r.FigaroID, transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}, nil
	}

	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, explicitID)
	cancel()
	if err == nil {
		ep := transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
		waitForSocket(ep.Address, 3*time.Second)
		return explicitID, ep, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return "", transport.Endpoint{}, fmt.Errorf("attach %q: %w", explicitID, err)
	}
	if !autoCreate {
		return "", transport.Endpoint{}, fmt.Errorf("no such aria: %s", explicitID)
	}

	createCtx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	defer ccancel()
	createResp, err := acli.CreateWithID(createCtx, explicitID, "", nil)
	if err != nil {
		return "", transport.Endpoint{}, fmt.Errorf("create %q: %w", explicitID, err)
	}
	ep := transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
	waitForSocket(ep.Address, 3*time.Second)
	return explicitID, ep, nil
}
