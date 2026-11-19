package config

import (
	"fmt"
	"strings"
	"unicode"
)

func ValidOutfitName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("outfit: empty name")
	case strings.ContainsAny(name, ",*?"):
		return fmt.Errorf("outfit: %q: a name cannot contain , * or ?", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("outfit: %q looks like a flag, not an outfit (--outfit takes a value)", name)
	case strings.ContainsAny(name, "=/\\"):
		return fmt.Errorf("outfit: %q: a name cannot contain = / or \\", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("outfit: %q: a name cannot begin with '.', the walk skips hidden files", name)
	case strings.ContainsAny(name, `{}[]"`):
		return fmt.Errorf("outfit: %q: a name cannot contain {} [] or a quote (an inline term must be a whole JSON object)", name)
	case strings.Contains(name, ":"):
		return fmt.Errorf("outfit: %q: a name cannot contain `:`: for a literal, quote it ('{\"a\":1}') or use the sugar (a=1)", name)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("outfit: %q: a name cannot contain whitespace", name)
		}
	}
	return nil
}
