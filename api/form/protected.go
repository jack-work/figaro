package form

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	ScopesKey = "system.scopes"

	GrantsKey = "system.grants"

	FanoutMaxChildrenKey = "system.fanout.max_children"

	FanoutMintKey = "system.fanout.mint"

	FanoutQuotaKey = "system.fanout.quota"
)

func ProtectedKeys() []string { return RootsOf(WriterLineage) }

func Protected(path string) (string, bool) { return rootOf(WriterLineage, path) }

func OwnScopes(s Snapshot) (raw json.RawMessage, ok bool, err error) {
	if grants, has := s.Get(GrantsKey); has && ShapeObject().Check(grants) != nil {
		return nil, false, fmt.Errorf("%s holds %s: a list there was the old name of %s and is no longer read; the owner unsets %s and sets %s to it", GrantsKey, clip(grants), ScopesKey, GrantsKey, ScopesKey)
	}
	raw, ok = s.Get(ScopesKey)
	return raw, ok, nil
}

func MoveListedGrants(s Snapshot) (p Patch, conflict bool) {
	grants, has := s.Get(GrantsKey)
	if !has || ShapeObject().Check(grants) == nil {
		return Patch{}, false
	}
	if s.Has(ScopesKey) {
		return Patch{}, true
	}
	return Build(s, map[string]json.RawMessage{ScopesKey: grants}, []string{GrantsKey}), false
}

func CheckScopes(s Snapshot) error {
	raw, ok, err := OwnScopes(s)
	if err != nil || !ok {
		return err
	}
	if err := checkScopeList(raw); err != nil {
		return fmt.Errorf("%s %w", ScopesKey, err)
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

func RefuseRetiredMintWrite(before, after Snapshot) error {
	now, ok := after.Get(FanoutMintKey)
	if was, _ := before.Get(FanoutMintKey); !ok || string(was) == string(now) {
		return nil
	}
	return fmt.Errorf("%s is retired while [authz] enforce_key_grants is on: the license to mint is the grant mint:* in %s, which a figaro gives only if it holds it", FanoutMintKey, ScopesKey)
}
