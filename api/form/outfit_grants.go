package form

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"
)

const OutfitGrantsKey = GrantsKey + ".outfits"

func ShapeOutfitGrants() Shape {
	return Shape{Name: "list of outfit names or globs", check: func(raw json.RawMessage) error {
		var l []string
		if json.Unmarshal(raw, &l) != nil || l == nil {
			return want("a list of outfit names or globs", raw)
		}
		for _, g := range l {
			if err := checkOutfitGrant(g); err != nil {
				return err
			}
		}
		return nil
	}}
}

func checkOutfitGrant(g string) error {
	if g == "" || strings.ContainsFunc(g, func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == '/' }) {
		return fmt.Errorf("%q is not an outfit name: no spaces, commas or slashes", g)
	}
	if _, err := path.Match(g, ""); err != nil {
		return fmt.Errorf("%q is not a glob: %w", g, err)
	}
	return nil
}

func OutfitGrants(s Snapshot) ([]string, error) {
	raw, ok := s.Get(OutfitGrantsKey)
	if !ok {
		return nil, nil
	}
	if err := ShapeOutfitGrants().Check(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", OutfitGrantsKey, err)
	}
	var l []string
	_ = json.Unmarshal(raw, &l)
	return l, nil
}

func OutfitGranted(grants []string, name string) bool {
	for _, g := range grants {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}
