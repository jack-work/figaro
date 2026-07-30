package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/transport"
)

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
