package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/transport"
)

// resolveTargetEndpoint resolves both id and endpoint. Used by verbs
// that talk to the figaro directly (send, plain, x, set, state...).
// Aria ids are system-minted, so a missing explicitID is always an
// error — there is no create-by-name. autoCreate is retained for call
// compatibility but no longer creates.
func resolveTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *angelus.Client, explicitID string, autoCreate bool, spec outfit.Spec) (string, transport.Endpoint, error) {
	if explicitID == "" {
		ppid := shellPID
		r, err := resolveBinding(ctx, acli, ppid)
		if err != nil {
			return "", transport.Endpoint{}, fmt.Errorf("resolve: %w", err)
		}
		if !r.Found {
			// autoCreate has been a parameter here since the function
			// existed, discarded by `_ = autoCreate` on the second line.
			// Every prompt path passes true and every read-only path passes
			// false, so the intent was written down and never honoured:
			// `send -f`, `-r` and `-v` all refused to work in a shell with
			// no binding, while a plain `send` in the same shell minted an
			// aria happily. Which flag you chose decided whether the verb
			// could create — and nothing said so.
			if !mintsWhenUnbound(autoCreate) {
				if resolveDisabled() {
					return "", transport.Endpoint{}, fmt.Errorf("no aria specified (pass --id <id>; binding disabled in this shell)")
				}
				return "", transport.Endpoint{}, fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
			}
			// Mint one, on the named outfit, and bind this shell to it —
			// bindBinding is a no-op when binding is disabled, so a script
			// gets the aria without acquiring a binding it never asked for.
			id, ep := mustCreateAndBindOutfit(ctx, acli, loaded, ppid, spec)
			return id, ep, nil
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

// mintsWhenUnbound is the create decision, pulled out so the truth table is
// testable without a daemon: a call that reached here has no --id and no
// binding, so the only question left is whether the verb is allowed to make
// one. Prompt verbs are; read-only verbs (hup, listen, outfit) are not,
// because "show me the aria" cannot sensibly answer by inventing one.
func mintsWhenUnbound(autoCreate bool) bool { return autoCreate }
