package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// Aria ids are system-minted, so a missing explicitID is an error; autoCreate
// decides whether an unbound shell mints.
func resolveTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, sdk.Addr, error) {
	id, ep, _, err := resolveTargetGrant(ctx, loaded, acli, explicitID, autoCreate, d)
	return id, ep, err
}

func resolveTargetGrant(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, sdk.Addr, rpc.AttachResponse, error) {
	var none rpc.AttachResponse
	if explicitID == "" {
		ppid := shellPID
		r, err := resolveBinding(ctx, acli, ppid)
		if err != nil {
			return "", sdk.Addr{}, none, fmt.Errorf("resolve: %w", err)
		}
		if !r.Found {
			if !mintsWhenUnbound(autoCreate) {
				if resolveDisabled() {
					return "", sdk.Addr{}, none, fmt.Errorf("no aria specified (pass --id <id>; binding disabled in this shell)")
				}
				return "", sdk.Addr{}, none, fmt.Errorf("no figaro bound to this shell (try: --id <id> or attend <id>)")
			}
			// bindBinding is a no-op when binding is disabled, so a
			// script gets the aria without a binding.
			id, ep := mustCreate(ctx, acli, loaded, d)
			attendNew(ctx, acli, id)
			return id, ep, none, nil
		}
		return r.FigaroID, ariaAddr(r.FigaroID), none, nil
	}

	if foreignNode(explicitID) != "" {
		return attachForeign(ctx, explicitID)
	}
	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, explicitID)
	cancel()
	if err == nil {
		return resp.FigaroID, ariaAddr(resp.FigaroID), *resp, nil
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not in tree") {
		return "", sdk.Addr{}, none, fmt.Errorf("no such aria: %s", explicitID)
	}
	return "", sdk.Addr{}, none, fmt.Errorf("attach %q: %w", explicitID, err)
}

func mintsWhenUnbound(autoCreate bool) bool { return autoCreate }

func attachForeign(ctx context.Context, ref string) (string, sdk.Addr, rpc.AttachResponse, error) {
	var none rpc.AttachResponse
	a, err := doorFor(ref)
	if err != nil {
		return "", sdk.Addr{}, none, err
	}
	acli, err := sdk.DialAngelus(a.Door)
	if err != nil {
		return "", sdk.Addr{}, none, err
	}
	defer acli.Close()
	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := acli.Attach(attachCtx, ref)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", sdk.Addr{}, none, fmt.Errorf("no such aria: %s", ref)
		}
		return "", sdk.Addr{}, none, fmt.Errorf("attach %q: %w", ref, err)
	}
	id := onNodeOf(ref, resp.FigaroID)
	return id, sdk.Addr{Door: a.Door, ID: id}, *resp, nil
}

func onNodeOf(ref, id string) string {
	node, _ := rpc.SplitNode(ref)
	if node == "" || strings.Contains(id, rpc.NodeSeparator) {
		return id
	}
	return node + rpc.NodeSeparator + id
}

// An unbound-form target is resolved through its role, late, per call; the
// `form` namespace never redirects.
func resolveFigaroTargetEndpoint(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, sdk.Addr, error) {
	id, ep, _, err := resolveFigaroTargetGrant(ctx, loaded, acli, explicitID, autoCreate, d)
	return id, ep, err
}

func resolveFigaroTargetGrant(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, explicitID string, autoCreate bool, d dressing) (string, sdk.Addr, rpc.AttachResponse, error) {
	id, ep, grant, err := resolveTargetGrant(ctx, loaded, acli, explicitID, autoCreate, d)
	if err != nil || (!strings.HasPrefix(id, "@") && !strings.Contains(id, "@")) {
		return id, ep, grant, err
	}
	return redirectRoleGrant(ctx, loaded, acli, id, ep)
}

// redirectRole follows a role form to its target aria once; a chain of roles is
// refused.
func redirectRole(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, id string, ep sdk.Addr) (string, sdk.Addr, error) {
	if !strings.HasPrefix(id, "@") && !strings.Contains(id, "@") {
		return id, ep, nil
	}
	id, ep, _, err := redirectRoleGrant(ctx, loaded, acli, id, ep)
	return id, ep, err
}

func redirectRoleGrant(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, id string, ep sdk.Addr) (string, sdk.Addr, rpc.AttachResponse, error) {
	var none rpc.AttachResponse
	role, err := roleAt(ctx, loaded, acli, id, ep)
	if err != nil {
		return "", sdk.Addr{}, none, err
	}
	if role.Refused != "" {
		return "", sdk.Addr{}, none, errors.New(role.Refused)
	}
	target := role.Holder
	if foreignNode(id) != "" {
		target = onNodeOf(id, target)
	}
	if role.Sandbox != "" {
		report(ctx, "role %s → aria %s, held in sandbox %s", ariaName(id), ariaName(target), role.Sandbox)
	} else {
		report(ctx, "role %s → aria %s", ariaName(id), ariaName(target))
	}
	return resolveTargetGrant(ctx, loaded, acli, target, false, dressing{})
}

func roleAt(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, id string, ep sdk.Addr) (rpc.RoleHolder, error) {
	if resp, err := attachRole(ctx, acli, id); err == nil && resp.Role != nil {
		return *resp.Role, nil
	}
	return roleOnForm(ctx, loaded, acli, id, ep)
}

func attachRole(ctx context.Context, acli *sdk.Angelus, id string) (rpc.AttachResponse, error) {
	if foreignNode(id) != "" {
		_, _, resp, err := attachForeign(ctx, id)
		return resp, err
	}
	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := acli.Attach(attachCtx, id)
	if err != nil {
		return rpc.AttachResponse{}, err
	}
	return *resp, nil
}

func roleOnForm(ctx context.Context, loaded *config.Loaded, acli *sdk.Angelus, id string, ep sdk.Addr) (rpc.RoleHolder, error) {
	var none rpc.RoleHolder
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return none, fmt.Errorf("read form %s: %w", id, err)
	}
	formCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := fcli.Form(formCtx)
	cancel()
	fcli.Close()
	if err != nil {
		return none, fmt.Errorf("read form %s: %w", id, err)
	}
	target := resp.Snapshot.Lookup("target-aria")
	if target == nil || *target == "" {
		return none, notARole{id}
	}
	if strings.Contains(*target, "@") {
		return rpc.RoleHolder{Refused: fmt.Sprintf("role %s: target-aria %q is a form, not an aria; roles do not chain", id, *target)}, nil
	}
	box := resp.Snapshot.Lookup("system.sandbox")
	if box == nil || *box == "" {
		return rpc.RoleHolder{Holder: *target}, nil
	}
	held := *target
	if foreignNode(id) != "" {
		held = onNodeOf(id, held)
	}
	tid, tep, _, err := resolveTargetGrant(ctx, loaded, acli, held, false, dressing{})
	if err != nil {
		return none, err
	}
	if got := sandboxOfAria(ctx, tep); got != *box {
		return rpc.RoleHolder{Refused: fmt.Sprintf("role %s is held in sandbox %s but points at %s, which runs in %q: refused", id, *box, ariaName(tid), got)}, nil
	}
	return rpc.RoleHolder{Holder: *target, Sandbox: *box}, nil
}

func sandboxOfAria(ctx context.Context, ep sdk.Addr) string {
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return ""
	}
	defer fcli.Close()
	formCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := fcli.Form(formCtx)
	if err != nil {
		return ""
	}
	if v := resp.Snapshot.Lookup("system.sandbox"); v != nil {
		return *v
	}
	return ""
}

type notARole struct{ id string }

func (e notARole) Error() string {
	return fmt.Sprintf("%s is a form, not a figaro: this verb needs one.\n  fig bind %s                 birth a figaro from it\n  fig set --id %s target-aria <aria>   make it a ROLE, and this verb reaches the holder", e.id, e.id, e.id)
}
