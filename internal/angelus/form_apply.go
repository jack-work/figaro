package angelus

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/formapply"
	"github.com/jack-work/figaro/internal/store"
)

type formApplyIDKey struct{}

func (h *handlers) formApply(ctx context.Context, params json.RawMessage) (any, error) {
	ctx, settle := withQuotaHolds(ctx)
	defer settle()
	var req rpc.FormApplyRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.FigaroID == "" || len(req.Outfits) == 0 {
		return nil, fmt.Errorf("form apply: an existing role ID and outfit names are required")
	}
	if !req.Check && (req.IfVersion == 0 || req.ExpectedOutfit == "") {
		return nil, fmt.Errorf("form apply: check first, then supply its version and outfit identity")
	}
	if declared, _ := ctx.Value(formApplyIDKey{}).(string); declared != req.FigaroID {
		return nil, fmt.Errorf("form apply: supply the exact existing role ID, not an alias or prefix")
	}
	if node, ok := h.angelus.Backend.Node(req.FigaroID); !ok || node.Kind != kindFormNode {
		return nil, fmt.Errorf("form apply: %s is not an existing unbound role", req.FigaroID)
	}
	if err := h.refuseBoxOutfits(ctx, req.Outfits, false); err != nil {
		return nil, err
	}
	raw, _, err := h.readFromStore(ctx, req.FigaroID, rpc.MethodForm, nil)
	if err != nil {
		return nil, err
	}
	current := raw.(rpc.FormResponse)
	if holder := current.Snapshot.Lookup("target-aria"); holder == nil || *holder == "" {
		return nil, fmt.Errorf("form apply: %s has no role holder", req.FigaroID)
	}
	outfit, err := h.dress(req.Outfits, nil)
	if err != nil {
		return nil, err
	}
	desired := form.Snapshot{}.Apply(outfit)
	patch, err := formapply.Plan(current.Snapshot, desired, req.Managed, managedHeldKeys(current.Snapshot), h.operatorPrefixes(ctx, params))
	if err != nil {
		return nil, err
	}
	identity, err := managedOutfitIdentity(desired, req.Managed)
	if err != nil {
		return nil, err
	}
	if req.ExpectedOutfit != "" && req.ExpectedOutfit != identity {
		return nil, fmt.Errorf("form apply: managed outfit values changed; check again")
	}
	ifVersion := current.Version
	if !req.Check {
		ifVersion = req.IfVersion
	}
	setParams, err := json.Marshal(rpc.SetRequest{Patch: patch, IfVersion: ifVersion})
	if err != nil {
		return nil, err
	}
	if err := h.refuseCallerPatch(ctx, rpc.MethodSet, req.FigaroID, setParams); err != nil {
		return nil, err
	}
	result := rpc.FormApplyResponse{FigaroID: req.FigaroID, Version: current.Version, Outfit: identity, Outcome: rpc.OutcomeUnchanged}
	for _, path := range req.Managed {
		a, have := current.Snapshot.Get(path)
		b, want := desired.Get(path)
		if have == want && (!have || form.NewValue(a).Equal(form.NewValue(b))) {
			continue
		}
		if want {
			result.Set = append(result.Set, path)
		} else {
			result.Remove = append(result.Remove, path)
		}
	}
	slices.Sort(result.Set)
	slices.Sort(result.Remove)
	if !patch.IsIdentity() {
		result.Outcome = rpc.OutcomeApplied
	}
	if req.Check {
		if !patch.IsIdentity() {
			result.Outcome = "would-apply"
		}
		return result, nil
	}
	if patch.IsIdentity() {
		write, err := h.angelus.Backend.ApplyFormWrite(req.FigaroID, store.FormWrite{Patch: patch, IfVersion: ifVersion})
		if err != nil {
			return nil, err
		}
		result.Version = write.Version
		return result, nil
	}
	setParams, err = rpc.WithAria(setParams, req.FigaroID)
	if err != nil {
		return nil, err
	}
	applied, err := h.ariaMethod(rpc.MethodSet)(ctx, setParams)
	if err != nil {
		return nil, err
	}
	response := applied.(rpc.SetResponse)
	result.Version, result.Outcome = response.Version, response.Outcome
	return result, nil
}

func managedOutfitIdentity(desired form.Snapshot, managed []string) (string, error) {
	paths := slices.Clone(managed)
	slices.Sort(paths)
	values := make(map[string]json.RawMessage, len(paths))
	for _, path := range paths {
		if value, ok := desired.Get(path); ok {
			values[path] = value
		}
	}
	raw, err := json.Marshal(struct {
		Managed []string                   `json:"managed"`
		Values  map[string]json.RawMessage `json:"values"`
	}{paths, values})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func managedHeldKeys(role form.Snapshot) []string {
	held := form.ProtectedKeys()
	for _, k := range boxHeldKeys {
		held = append(held, k.key)
	}
	handoff := nested(role, successionPolicyKey, "handoff-key")
	if handoff == "" {
		handoff = successionDefaultHandoff
	}
	return append(held, handoff)
}

func (h *handlers) operatorPrefixes(ctx context.Context, params json.RawMessage) []string {
	if _, boxed := callerBox(ctx); boxed || authz.FigaroOf(ctx) != "" || rpc.CallerOf(params) != "" || !h.callerIsOwner(ctx) {
		return nil
	}
	return slices.Clone(formapply.OperatorPrefixes)
}
