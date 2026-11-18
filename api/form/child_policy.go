package form

import (
	"encoding/json"
	"fmt"
	"sort"
)

const ChildPolicyKey = "system.child_policy"

var defaultChildScopes = json.RawMessage(`["send:parent","read:aria:self","write:aria:self#@self","delete:aria:self#@self"]`)

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
		case WriterSelf, WriterLineage, WriterOwner:
		default:
			return nil, true, fmt.Errorf("%s: %s is %s class; a child policy carries only self-, lineage- and owner-class keys", ChildPolicyKey, k, w)
		}
	}
	if faults := TouchedShapeFaults(s.Apply(Build(s, keys, nil)), names); len(faults) > 0 {
		return nil, true, fmt.Errorf("%s: a child would be born with %s: %w", ChildPolicyKey, faults[0].Path, faults[0].Err)
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
	p := Build(Snapshot{}, keys, nil)
	if _, own, over := policyFanoutOver(parent, p); over {
		raw, _ := json.Marshal(own)
		p = Merge(p, Build(Snapshot{}, map[string]json.RawMessage{FanoutMaxChildrenKey: raw}, nil))
	}
	return p, nil
}

func policyFanoutOver(holder Snapshot, policy Patch) (n, own int, over bool) {
	n, carried, _ := FanoutMaxChildren(Snapshot{}.Apply(policy))
	if !carried {
		return 0, 0, false
	}
	own, capped, err := FanoutMaxChildren(holder)
	if err != nil || !capped {
		return n, 0, false
	}
	return n, own, n > own
}

func ChildPolicyOwnerKeysChanged(before, after Snapshot) []string {
	was, _, _ := ChildPolicy(before)
	now, _, _ := ChildPolicy(after)
	var out []string
	for k, v := range now {
		if WriterOf(k) == WriterOwner && string(was[k]) != string(v) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func CheckChildPolicyFanout(holder Snapshot) error {
	keys, set, err := ChildPolicy(holder)
	if err != nil || !set {
		return err
	}
	if n, own, over := policyFanoutOver(holder, Build(Snapshot{}, keys, nil)); over {
		return fmt.Errorf("%s: a child would hold %s %d, more than this figaro's own %d; a figaro gives only what it holds", ChildPolicyKey, FanoutMaxChildrenKey, n, own)
	}
	return nil
}
