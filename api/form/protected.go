package form

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	ScopesKey = "system.scopes"

	RetiredGrantsKey = "system.grants"

	FanoutMaxChildrenKey = "system.fanout.max_children"

	FanoutMintKey = "system.fanout.mint"

	FanoutQuotaKey = "system.fanout.quota"
)

func ProtectedKeys() []string { return RootsOf(WriterLineage) }

func Protected(path string) (string, bool) { return rootOf(WriterLineage, path) }

func OwnScopes(s Snapshot) (raw json.RawMessage, key string, ok bool, err error) {
	scopes, hasScopes := s.Get(ScopesKey)
	grants, hasGrants := s.Get(RetiredGrantsKey)
	switch {
	case hasScopes && hasGrants:
		return nil, "", false, fmt.Errorf("%s and %s are both set: %s is the old name for %s; unset it", ScopesKey, RetiredGrantsKey, RetiredGrantsKey, ScopesKey)
	case hasGrants:
		return grants, RetiredGrantsKey, true, nil
	case hasScopes:
		return scopes, ScopesKey, true, nil
	}
	return nil, "", false, nil
}

func CheckScopes(s Snapshot) error {
	raw, key, ok, err := OwnScopes(s)
	if err != nil || !ok {
		return err
	}
	if err := checkScopeList(raw); err != nil {
		return fmt.Errorf("%s %w", key, err)
	}
	return nil
}

func checkScopeList(raw json.RawMessage) error {
	var scopes []string
	if err := json.Unmarshal(raw, &scopes); err != nil || scopes == nil {
		return fmt.Errorf("must be a JSON array of strings, got %s", raw)
	}
	for _, g := range scopes {
		if g == "" || strings.TrimSpace(g) != g {
			return fmt.Errorf("has %q, which is empty or has surrounding whitespace", g)
		}
	}
	return nil
}

var (
	maxChildrenShape = ShapeInt(0, math.MaxInt64)
	mintShape        = ShapeBool()
	quotaShape       = ShapeInt(0, math.MaxInt64)
)

func FanoutMaxChildren(s Snapshot) (n int, ok bool, err error) {
	raw, has := s.Get(FanoutMaxChildrenKey)
	if !has {
		return 0, false, nil
	}
	if maxChildrenShape.Check(raw) != nil || json.Unmarshal(raw, &n) != nil {
		return 0, false, fmt.Errorf("%s must be a non-negative integer, got %s", FanoutMaxChildrenKey, raw)
	}
	return n, true, nil
}

func FanoutQuota(s Snapshot) (n int, ok bool, err error) {
	raw, has := s.Get(FanoutQuotaKey)
	if !has {
		return 0, false, nil
	}
	if quotaShape.Check(raw) != nil || json.Unmarshal(raw, &n) != nil {
		return 0, false, fmt.Errorf("%s must be a non-negative integer, got %s", FanoutQuotaKey, raw)
	}
	return n, true, nil
}

func FanoutMint(s Snapshot) (bool, error) {
	raw, has := s.Get(FanoutMintKey)
	if !has {
		return false, nil
	}
	var b bool
	if mintShape.Check(raw) != nil || json.Unmarshal(raw, &b) != nil {
		return false, fmt.Errorf("%s must be true or false, got %s", FanoutMintKey, raw)
	}
	return b, nil
}

func RefuseRetiredGrantsWrite(before, after Snapshot) error {
	was, _ := before.Get(RetiredGrantsKey)
	now, ok := after.Get(RetiredGrantsKey)
	if !ok || string(was) == string(now) {
		return nil
	}
	return fmt.Errorf("%s is now %s: set that instead (figaro set %s drops %s in the same write)", RetiredGrantsKey, ScopesKey, ScopesKey, RetiredGrantsKey)
}
