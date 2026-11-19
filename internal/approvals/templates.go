package approvals

import (
	"encoding/json"
	"regexp"
)

type verbTemplate struct {
	target         *regexp.Regexp
	targetKind     string
	keys           []string
	nextSuccession bool
	validate       func(set map[string]json.RawMessage) (map[string]json.RawMessage, error)
}

var templates = map[string]verbTemplate{
	VerbFigaroModel: {target: ariaRe, targetKind: "full figaro id (32 hex)", keys: modelKeys, validate: validateModel},
	VerbFigaroTTL:   {target: ariaRe, targetKind: "full figaro id (32 hex)", keys: []string{"system.ttl"}, validate: validateTTL},
	VerbRoleModel:   {target: roleRe, targetKind: "role form id (@hex)", keys: modelKeys, validate: validateModel, nextSuccession: true},
}

var modelKeys = []string{"system.provider", "system.model"}

func TakesEffectAtSuccession(verb string) bool { return templates[verb].nextSuccession }

func validateTTL(set map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	ttl, err := plainString(set["system.ttl"], ttlRe, "system.ttl")
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(ttl)
	return map[string]json.RawMessage{"system.ttl": b}, nil
}

func validateModel(set map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	provider, err := plainString(set["system.provider"], providerRe, "system.provider")
	if err != nil {
		return nil, err
	}
	model, err := plainString(set["system.model"], modelRe, "system.model")
	if err != nil {
		return nil, err
	}
	p, _ := json.Marshal(provider)
	m, _ := json.Marshal(model)
	return map[string]json.RawMessage{"system.provider": p, "system.model": m}, nil
}
