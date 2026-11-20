package form

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	DelegateKey = "system.delegate"

	DelegateRolesKey = "system.delegate_roles"

	LifecycleStatusKey = "lifecycle.status"

	RoutingAlias = "routing-alias"
)

var delegable = []string{"system.model", "system.provider", FanoutMaxChildrenKey, FanoutQuotaKey}

func DelegableKeys() []string { return slices.Clone(delegable) }

func ShapeDelegate() Shape {
	return Shape{Name: "list of " + strings.Join(delegable, "|"), check: func(raw json.RawMessage) error {
		var l []string
		if json.Unmarshal(raw, &l) != nil || l == nil {
			return want("a list of strings", raw)
		}
		for _, k := range l {
			if !slices.Contains(delegable, k) {
				return fmt.Errorf("%q may not be delegated: only %s", k, strings.Join(delegable, ", "))
			}
		}
		return nil
	}}
}

type Delegation struct {
	Keys  []string
	Roles []string
}

func DelegationOf(s Snapshot) Delegation {
	if v := s.Lookup(LifecycleStatusKey); v != nil && *v == RoutingAlias {
		return Delegation{}
	}
	var d Delegation
	if raw, ok := s.Get(DelegateKey); ok && ShapeDelegate().Check(raw) == nil {
		_ = json.Unmarshal(raw, &d.Keys)
	}
	if raw, ok := s.Get(DelegateRolesKey); ok && ShapeStringList().Check(raw) == nil {
		_ = json.Unmarshal(raw, &d.Roles)
	}
	return d
}

func (d Delegation) Delegates(key string) bool { return slices.Contains(d.Keys, key) }
