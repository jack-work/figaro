package form

import (
	"encoding/json"
	"fmt"
	"sort"
)

const ChildPolicyKey = "system.child_policy"

var defaultChildScopes = json.RawMessage(`["send:parent","read:aria:self"]`)

func DefaultChildPolicy() map[string]json.RawMessage {
	return map[string]json.RawMessage{ScopesKey: defaultChildScopes}
}

func ChildPolicy(s Snapshot) (map[string]json.RawMessage, bool, error) {
	raw, ok := s.Get(ChildPolicyKey)
	if !ok {
		return nil, false, nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return nil, true, fmt.Errorf("%s must be an object of key paths to values, got %s", ChildPolicyKey, raw)
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		switch w := WriterOf(k); w {
		case WriterSelf, WriterLineage:
		default:
			return nil, true, fmt.Errorf("%s: %s is %s class; a child policy carries only self- and lineage-class keys", ChildPolicyKey, k, w)
		}
	}
	return keys, true, nil
}

func ChildPolicyPatch(parent Snapshot, figaroMade bool) (Patch, error) {
	keys, set, err := ChildPolicy(parent)
	if err != nil {
		return Patch{}, err
	}
	if !set {
		if !figaroMade {
			return Patch{}, nil
		}
		keys = DefaultChildPolicy()
	}
	return Build(Snapshot{}, keys, nil), nil
}
