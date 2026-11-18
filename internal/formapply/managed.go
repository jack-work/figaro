package formapply

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

var OperatorPrefixes = form.OperatorPaths()

func Plan(current, desired form.Snapshot, managed, held, operator []string) (form.Patch, error) {
	if len(managed) == 0 {
		return form.Patch{}, fmt.Errorf("form apply: name at least one managed path")
	}
	paths := slices.Clone(managed)
	slices.Sort(paths)
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if err := validatePath(path, operator); err != nil {
			return form.Patch{}, err
		}
		for _, key := range held {
			if overlaps(path, key) && !under(key, operator) {
				return form.Patch{}, fmt.Errorf("form apply: %q covers %q, which the daemon holds", path, key)
			}
		}
		parts := strings.Split(path, ".")
		for n := 1; n <= len(parts); n++ {
			prefix := strings.Join(parts[:n], ".")
			if seen[prefix] {
				return form.Patch{}, fmt.Errorf("form apply: managed paths %q and %q overlap", prefix, path)
			}
			if n == len(parts) {
				continue
			}
			for _, snapshot := range []form.Snapshot{current, desired} {
				if value, ok := snapshot.Get(prefix); ok && !strings.HasPrefix(strings.TrimSpace(string(value)), "{") {
					return form.Patch{}, fmt.Errorf("form apply: managed path %q crosses non-object %q", path, prefix)
				}
			}
		}
		seen[path] = true
	}
	set := make(map[string]json.RawMessage, len(paths))
	var remove []string
	for _, path := range paths {
		if value, ok := desired.Get(path); ok {
			set[path] = value
		} else if current.Has(path) {
			remove = append(remove, path)
		}
	}
	return form.Build(current, set, remove), nil
}

func validatePath(path string, operator []string) error {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, "[]*\\\x00\r\n\t") {
		return fmt.Errorf("form apply: invalid managed path %q", path)
	}
	parts := strings.Split(path, ".")
	for _, part := range parts {
		if part == "" || part != strings.TrimSpace(part) {
			return fmt.Errorf("form apply: invalid managed path %q", path)
		}
	}
	if under(path, operator) {
		return nil
	}
	root := parts[0]
	if strings.HasPrefix(root, "comment_") || strings.HasPrefix(root, "thread_") {
		return fmt.Errorf("form apply: %q is runtime-owned", path)
	}
	switch root {
	case "aria_id", "target-aria", "handoff", "system", "comments", "credentials", "api_key", "service_tokens", "permissions", "scopes", "grants":
		return fmt.Errorf("form apply: %q is runtime-owned", path)
	}
	return nil
}

func under(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+".") {
			return true
		}
	}
	return false
}

func overlaps(path, key string) bool {
	return path == key || strings.HasPrefix(path, key+".") || strings.HasPrefix(key, path+".")
}
