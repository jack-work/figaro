package form

import (
	"encoding/json"
	"fmt"
	"strings"
)

const GrantsKey = "system.grants"

var protectedKeys = []string{GrantsKey, "system.sandbox"}

func ProtectedKeys() []string { return append([]string(nil), protectedKeys...) }

func Protected(path string) (string, bool) {
	for _, k := range protectedKeys {
		if path == k || strings.HasPrefix(path, k+".") || strings.HasPrefix(path, k+"[") {
			return k, true
		}
	}
	return "", false
}

func CheckGrants(s Snapshot) error {
	raw, ok := s.Get(GrantsKey)
	if !ok {
		return nil
	}
	var grants []string
	if err := json.Unmarshal(raw, &grants); err != nil || grants == nil {
		return fmt.Errorf("%s must be a JSON array of strings, got %s", GrantsKey, raw)
	}
	for _, g := range grants {
		if g == "" || strings.TrimSpace(g) != g {
			return fmt.Errorf("%s: grant %q is empty or has surrounding whitespace", GrantsKey, g)
		}
	}
	return nil
}
