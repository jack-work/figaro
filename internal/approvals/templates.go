package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

type verbTemplate struct {
	target         *regexp.Regexp
	targetKind     string
	keys           []string
	nextSuccession bool
	validate       func(ctx context.Context, f Forms, set map[string]json.RawMessage) (map[string]json.RawMessage, map[string]string, error)
}

var templates = map[string]verbTemplate{
	VerbFigaroModel: {target: ariaRe, targetKind: "full figaro id (32 hex)", keys: modelKeys, validate: validateModel},
	VerbFigaroTTL:   {target: ariaRe, targetKind: "full figaro id (32 hex)", keys: []string{"system.ttl"}, validate: validateTTL},
	VerbRoleModel:   {target: roleRe, targetKind: "role form id (@hex)", keys: modelKeys, validate: validateModel, nextSuccession: true},
	VerbRoleOutfits: {target: roleRe, targetKind: "role form id (@hex)", keys: []string{"system.grants.outfits"}, validate: validateOutfits},
}

var modelKeys = []string{"system.provider", "system.model"}

func TakesEffectAtSuccession(verb string) bool { return templates[verb].nextSuccession }

func validateTTL(_ context.Context, _ Forms, set map[string]json.RawMessage) (map[string]json.RawMessage, map[string]string, error) {
	ttl, err := plainString(set["system.ttl"], ttlRe, "system.ttl")
	if err != nil {
		return nil, nil, err
	}
	b, _ := json.Marshal(ttl)
	return map[string]json.RawMessage{"system.ttl": b}, nil, nil
}

func validateModel(_ context.Context, _ Forms, set map[string]json.RawMessage) (map[string]json.RawMessage, map[string]string, error) {
	provider, err := plainString(set["system.provider"], providerRe, "system.provider")
	if err != nil {
		return nil, nil, err
	}
	model, err := plainString(set["system.model"], modelRe, "system.model")
	if err != nil {
		return nil, nil, err
	}
	p, _ := json.Marshal(provider)
	m, _ := json.Marshal(model)
	return map[string]json.RawMessage{"system.provider": p, "system.model": m}, nil, nil
}

func validateOutfits(ctx context.Context, f Forms, set map[string]json.RawMessage) (map[string]json.RawMessage, map[string]string, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(set["system.grants.outfits"], &raws); err != nil {
		return nil, nil, fmt.Errorf("%w: system.grants.outfits must be a list of outfit names", ErrInvalid)
	}
	if len(raws) == 0 || len(raws) > 16 {
		return nil, nil, fmt.Errorf("%w: system.grants.outfits holds 1 to 16 names", ErrInvalid)
	}
	names := make([]string, 0, len(raws))
	inputs := map[string]string{}
	for _, r := range raws {
		name, err := plainString(r, outfitRe, "outfit name")
		if err != nil {
			return nil, nil, err
		}
		if slices.Contains(names, name) {
			return nil, nil, fmt.Errorf("%w: outfit %q named twice", ErrInvalid, name)
		}
		digest, ok, err := f.Outfit(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, fmt.Errorf("%w: no outfit %q", ErrInvalid, name)
		}
		names = append(names, name)
		inputs["outfit:"+name] = digest
	}
	b, _ := json.Marshal(names)
	return map[string]json.RawMessage{"system.grants.outfits": b}, inputs, nil
}
