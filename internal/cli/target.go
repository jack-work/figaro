package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// Aria ids are system-minted, so a missing explicitID is an error; autoCreate
// decides whether an unbound shell mints.
func resolveTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, sdk.Addr, error) {
	if explicitID == "" {
		ppid := shellPID
		r, err := resolveBinding(ctx, acli, ppid)
		if err != nil {
			return "", sdk.Addr{}, fmt.Errorf("resolve: %w", err)
		}
		if !r.Found {
			if !mintsWhenUnbound(autoCreate) {
				if resolveDisabled() {
					return "", sdk.Addr{}, fmt.Errorf("no aria specified (pass --id <id>; binding disabled in this shell)")
				}
				return "", sdk.Addr{}, fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
			}
			// bindBinding is a no-op when binding is disabled, so a
			// script gets the aria without a binding.
			id, ep := mustCreate(ctx, acli, loaded, d)
			attendNew(ctx, acli, id)
			return id, ep, nil
		}
		return r.FigaroID, ariaAddr(r.FigaroID), nil
	}

	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, explicitID)
	cancel()
	if err == nil {
		return resp.FigaroID, ariaAddr(resp.FigaroID), nil
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not in tree") {
		return "", sdk.Addr{}, fmt.Errorf("no such aria: %s", explicitID)
	}
	return "", sdk.Addr{}, fmt.Errorf("attach %q: %w", explicitID, err)
}

// Prompt verbs mint when unbound; read-only verbs (hup, listen, outfit) do not.
func mintsWhenUnbound(autoCreate bool) bool { return autoCreate }

// An unbound-form target is resolved through its role, late, per call; the
// `form` namespace never redirects.
func resolveFigaroTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, sdk.Addr, error) {
	id, ep, err := resolveTargetEndpoint(ctx, loaded, acli, explicitID, autoCreate, d)
	if err != nil {
		return id, ep, err
	}
	return redirectRole(ctx, loaded, acli, id, ep)
}

// redirectRole follows a role form to its target aria once; a chain of roles is
// refused.
func redirectRole(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, id string, ep sdk.Addr) (string, sdk.Addr, error) {
	if !strings.HasPrefix(id, "@") && !strings.Contains(id, "@") {
		return id, ep, nil
	}
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return "", sdk.Addr{}, fmt.Errorf("read form %s: %w", id, err)
	}
	formCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := fcli.Form(formCtx)
	cancel()
	fcli.Close()
	if err != nil {
		return "", sdk.Addr{}, fmt.Errorf("read form %s: %w", id, err)
	}
	target := resp.Snapshot.Lookup("target-aria")
	if target == nil || *target == "" {
		return "", sdk.Addr{}, fmt.Errorf("%s is a form, not a figaro: this verb needs one.\n  fig bind %s                 birth a figaro from it\n  fig set --id %s target-aria <aria>   make it a ROLE, and this verb reaches the holder", id, id, id)
	}
	if strings.Contains(*target, "@") {
		return "", sdk.Addr{}, fmt.Errorf("role %s: target-aria %q is a form, not an aria; roles do not chain", id, *target)
	}
	report(ctx, "role %s → aria %s", ariaName(id), ariaName(*target))
	return resolveTargetEndpoint(ctx, loaded, acli, *target, false, dressing{})
}
