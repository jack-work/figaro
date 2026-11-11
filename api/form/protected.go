package form

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ScopesKey = "system.scopes"

	RetiredGrantsKey = "system.grants"
)

var protectedKeys = []string{ScopesKey, RetiredGrantsKey, "system.sandbox"}

func ProtectedKeys() []string { return append([]string(nil), protectedKeys...) }

func Protected(path string) (string, bool) {
	for _, k := range protectedKeys {
		if path == k || strings.HasPrefix(path, k+".") || strings.HasPrefix(path, k+"[") {
			return k, true
		}
	}
	return "", false
}

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
	var scopes []string
	if err := json.Unmarshal(raw, &scopes); err != nil || scopes == nil {
		return fmt.Errorf("%s must be a JSON array of strings, got %s", key, raw)
	}
	for _, g := range scopes {
		if g == "" || strings.TrimSpace(g) != g {
			return fmt.Errorf("%s: %q is empty or has surrounding whitespace", key, g)
		}
	}
	return nil
}

func RefuseRetiredGrantsWrite(before, after Snapshot) error {
	was, _ := before.Get(RetiredGrantsKey)
	now, ok := after.Get(RetiredGrantsKey)
	if !ok || string(was) == string(now) {
		return nil
	}
	return fmt.Errorf("%s is now %s: set that instead (figaro set %s drops %s in the same write)", RetiredGrantsKey, ScopesKey, ScopesKey, RetiredGrantsKey)
}
