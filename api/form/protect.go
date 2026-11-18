package form

import (
	"fmt"
	"sort"
	"strings"
)

// CheckWritable refuses an unprivileged write to a system-managed key.
func CheckWritable(p Patch, privileged bool) error {
	if privileged {
		return nil
	}
	var bad []string
	for _, e := range p.Entries() {
		if _, ok := rootOf(WriterRuntime, e.Key); ok {
			bad = append(bad, e.Key)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s: written by the harness, not by hand", strings.Join(bad, ", "))
}

func SystemManagedKeys() []string {
	out := RootsOf(WriterRuntime)
	sort.Strings(out)
	return out
}
