package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
)

// resolveTargetEndpoint resolves both id and endpoint. Used by verbs
// that talk to the figaro directly (send, plain, x, set, state...).
// Aria ids are system-minted, so a missing explicitID is always an
// error: there is no create-by-name. autoCreate is retained for call
// compatibility but no longer creates.
func resolveTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, transport.Endpoint, error) {
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
			// could create, and nothing said so.
			if !mintsWhenUnbound(autoCreate) {
				if resolveDisabled() {
					return "", transport.Endpoint{}, fmt.Errorf("no aria specified (pass --id <id>; binding disabled in this shell)")
				}
				return "", transport.Endpoint{}, fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
			}
			// Mint one, on the named outfit, and bind this shell to it -
			// bindBinding is a no-op when binding is disabled, so a script
			// gets the aria without acquiring a binding it never asked for.
			id, ep := mustCreateAndBindOutfit(ctx, acli, loaded, ppid, d)
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

// resolveFigaroTargetEndpoint is resolveTargetEndpoint for verbs that need
// a FIGARO (send, listen, hup, queue): an unbound-form target is resolved
// through its role. The `form` namespace never redirects: set, state,
// form listen, state outfit address the form itself and use the raw
// resolver. Resolution is LATE, per call: repoint target-aria and the
// next invocation reaches the successor; nothing chases mid-stream.
func resolveFigaroTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, transport.Endpoint, error) {
	id, ep, err := resolveTargetEndpoint(ctx, loaded, acli, explicitID, autoCreate, d)
	if err != nil {
		return id, ep, err
	}
	return redirectRole(ctx, loaded, acli, id, ep)
}

// redirectRole follows a role form to its target aria, once. A plain
// form (no target-aria) refuses by name; a target that is itself a form
// refuses too: target-aria names an aria, and a chain of roles is a
// misconfiguration better reported than walked.
func redirectRole(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, id string, ep transport.Endpoint) (string, transport.Endpoint, error) {
	if !strings.HasPrefix(id, "@") && !strings.Contains(id, "@") {
		return id, ep, nil
	}
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return "", transport.Endpoint{}, fmt.Errorf("read form %s: %w", id, err)
	}
	formCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := fcli.Form(formCtx)
	cancel()
	fcli.Close()
	if err != nil {
		return "", transport.Endpoint{}, fmt.Errorf("read form %s: %w", id, err)
	}
	target := resp.Snapshot.Lookup("target-aria")
	if target == nil || *target == "" {
		return "", transport.Endpoint{}, fmt.Errorf("%s is a form, not a figaro: this verb needs one.\n  fig bind %s                 birth a figaro from it\n  fig set --id %s target-aria <aria>   make it a ROLE, and this verb reaches the holder", id, id, id)
	}
	if strings.Contains(*target, "@") {
		return "", transport.Endpoint{}, fmt.Errorf("role %s: target-aria %q is a form, not an aria; roles do not chain", id, *target)
	}
	fmt.Fprintf(os.Stderr, "role %s → aria %s\n", id, *target)
	return resolveTargetEndpoint(ctx, loaded, acli, *target, false, dressing{})
}
