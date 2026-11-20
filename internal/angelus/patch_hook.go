package angelus

import (
	"context"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
)

type patchMode uint8

const (
	patchJudged patchMode = iota
	patchOwnerShapes
	patchOwnerContext
)

type formWrite struct {
	line   lineage
	before form.Snapshot
	patch  *form.Patch
	mode   patchMode
}

type patchCheck struct {
	h      *handlers
	ctx    context.Context
	line   lineage
	before form.Snapshot
	patch  form.Patch
	after  form.Snapshot
	caller callerKeys
}

func (c *patchCheck) owner() bool { return c.caller.owns && !c.caller.boxed }

type patchScope uint8

const (
	anyWriter patchScope = iota
	ownerOnly
	notOwner
)

type patchRule struct {
	scope patchScope
	check func(*patchCheck) error
}

var everyWriteRules []patchRule

var ownerShapeRules = []patchRule{
	{check: func(c *patchCheck) error { return refuseRuntimeKeys(c.before, c.patch) }},
	{check: func(c *patchCheck) error { return refuseMisshapenKeys(c.after, c.patch) }},
}

var ownerContextRules = []patchRule{
	{check: func(c *patchCheck) error { return refuseMisshapenKeys(c.after, c.patch) }},
}

var judgedRules = []patchRule{
	{check: func(c *patchCheck) error { return refuseReapKeys(c.before, c.patch) }},
	{check: func(c *patchCheck) error { return refuseRuntimeKeys(c.before, c.patch) }},
	{check: func(c *patchCheck) error { return refuseMalformedRetirement(c.before, c.patch) }},
	{check: func(c *patchCheck) error { return form.CheckScopes(c.after) }},
	{check: func(c *patchCheck) error { return checkScopesParse(c.before, c.after) }},
	{check: func(c *patchCheck) error {
		_, _, err := form.FanoutMaxChildren(c.after)
		return err
	}},
	{check: func(c *patchCheck) error {
		_, err := form.FanoutMint(c.after)
		return err
	}},
	{check: func(c *patchCheck) error {
		if !c.h.angelus.Policies.Enforces(authz.KeyGrants) {
			return nil
		}
		return form.RefuseRetiredMintWrite(c.before, c.after)
	}},
	{check: func(c *patchCheck) error {
		_, _, err := form.FanoutQuota(c.after)
		return err
	}},
	{check: func(c *patchCheck) error { return refuseMisshapenKeys(c.after, c.patch) }},
	{check: (*patchCheck).refuseChildPolicy},
	{check: func(c *patchCheck) error {
		return c.h.refuseKeysBeyondGrants(c.ctx, c.line, c.before, c.patch, c.after)
	}},
	{scope: ownerOnly, check: func(c *patchCheck) error {
		return c.h.refuseFanoutByLabelAlone(c.ctx, c.line, c.before, c.after)
	}},
	{scope: ownerOnly, check: func(c *patchCheck) error {
		return c.h.refuseQuotaByLabelAlone(c.ctx, c.line, c.before, c.after)
	}},
	{scope: ownerOnly, check: func(c *patchCheck) error {
		return c.h.refuseMintByLabelAlone(c.ctx, c.line, c.before, c.after)
	}},
	{scope: notOwner, check: (*patchCheck).refuseWrittenKeys},
	{scope: notOwner, check: func(c *patchCheck) error {
		return c.h.refuseScopesBeyondWriter(c.ctx, c.caller, c.line, c.before, c.after)
	}},
	{scope: notOwner, check: func(c *patchCheck) error { return c.h.refuseFanoutBeyondGranter(c.ctx, c.before, c.after) }},
	{scope: notOwner, check: func(c *patchCheck) error {
		return c.h.refuseQuotaBeyondBudget(c.ctx, c.line, c.before, c.after)
	}},
	{scope: notOwner, check: func(c *patchCheck) error { return c.h.refuseMintBeyondGranter(c.ctx, c.before, c.after) }},
}

func (c *patchCheck) refuseChildPolicy() error {
	if !keyChanged(c.before, c.after, form.ChildPolicyKey) {
		return nil
	}
	if err := form.CheckChildPolicyFanout(c.after); err != nil {
		return err
	}
	if c.owner() {
		return nil
	}
	if keys := form.ChildPolicyOwnerKeysChanged(c.before, c.after); len(keys) > 0 {
		return fmt.Errorf("%s: %s is owner-held: only the owner puts it in a child policy", form.ChildPolicyKey, keys[0])
	}
	return nil
}

func (c *patchCheck) refuseWrittenKeys() error {
	for _, p := range writtenPaths(c.before, c.patch, c.after) {
		if k, ok := form.Protected(p); ok && c.caller.lineage {
			if err := c.h.refuseProtected(c.ctx, c.caller, c.line, k, c.before); err != nil {
				return err
			}
			continue
		}
		if retentionGranted(c.ctx, c.before, p) {
			continue
		}
		if c.h.toolShortensLifetime(c.ctx, c.caller, c.line, c.before, c.after, p) {
			continue
		}
		if err := c.caller.refusal(p); err != nil {
			return err
		}
	}
	return nil
}

func (m patchMode) rules() []patchRule {
	switch m {
	case patchOwnerShapes:
		return ownerShapeRules
	case patchOwnerContext:
		return ownerContextRules
	}
	return judgedRules
}

func (h *handlers) judgeWrite(ctx context.Context, w formWrite) error {
	err := h.applyPatchRules(ctx, w)
	if w.mode != patchJudged {
		return err
	}
	p := h.angelus.Policies
	if err == nil || p.Enforces(authz.KeyGrants) {
		return err
	}
	if pre := h.applyPatchRules(context.WithValue(ctx, waivedKeyGrants{}, true), w); pre != nil {
		return pre
	}
	p.WouldRefuse(authz.KeyGrants, err.Error())
	return nil
}

func (h *handlers) applyPatchRules(ctx context.Context, w formWrite) error {
	if w.patch == nil || w.patch.IsIdentity() {
		return nil
	}
	c := &patchCheck{h: h, ctx: ctx, line: w.line, before: w.before, patch: *w.patch, after: w.before.Apply(*w.patch), caller: h.callerKeysOf(ctx)}
	for _, r := range slices.Concat(everyWriteRules, w.mode.rules()) {
		if r.scope == ownerOnly && !c.owner() || r.scope == notOwner && c.owner() {
			continue
		}
		if err := r.check(c); err != nil {
			return err
		}
	}
	return nil
}
