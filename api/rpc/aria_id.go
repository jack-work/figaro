package rpc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/handle"
)

const MaxAriaIDLen = 64

// ValidateAriaID enforces [A-Za-z0-9_-@]{1,64}: '@' is the unbound-form
// sigil, and node ids are one namespace.
func ValidateAriaID(id string) error {
	if len(id) == 0 {
		return fmt.Errorf("aria id is empty")
	}
	if len(id) > MaxAriaIDLen {
		return fmt.Errorf("aria id too long: %d chars (max %d)", len(id), MaxAriaIDLen)
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		case r == '@':
		default:
			return fmt.Errorf("aria id: invalid char %q at position %d (allowed: [A-Za-z0-9_-@])", r, i)
		}
	}
	return nil
}

const NodeSeparator = "/"

func ValidateAriaRef(ref string) error {
	node, id := SplitNode(ref)
	if node != "" {
		if err := ValidateAriaID(node); err != nil {
			return fmt.Errorf("node of %q: %w", ref, err)
		}
	}
	return ValidateAriaID(id)
}

func SplitNode(ref string) (node, id string) {
	if i := strings.Index(ref, NodeSeparator); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

const (
	ShortIDLen   = handle.HexLen
	OldIDLen     = 8
	NodeShortLen = 8
)

func ShortIDs(ids []string) map[string]string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	out := make(map[string]string, len(sorted))
	for i, id := range sorted {
		if !IsHexID(id) {
			out[id] = id
			continue
		}
		sigil := len(id) - len(strings.TrimPrefix(id, "@"))
		n := sigil + ShortIDLen
		if i > 0 {
			n = max(n, commonPrefixLen(id, sorted[i-1])+1)
		}
		if i+1 < len(sorted) {
			n = max(n, commonPrefixLen(id, sorted[i+1])+1)
		}
		if n-sigil == OldIDLen && len(id)-sigil > OldIDLen {
			n++
		}
		out[id] = id[:min(n, len(id))]
	}
	return out
}

func commonPrefixLen(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

func IsHexID(id string) bool {
	id = strings.TrimPrefix(id, "@")
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
