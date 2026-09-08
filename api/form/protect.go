package form

// Protection: which keys a caller off the wire may write.

import (
	"fmt"
	"strings"
)

// systemManaged is the set of keys the harness owns, indexed once.
var systemManaged = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range WellKnownKeys() {
		if k.Mode == KeySystemManaged {
			m[k.Key] = true
		}
	}
	return m
}()

// CheckWritable refuses an unprivileged write to a system-managed key.
func CheckWritable(p Patch, privileged bool) error {
	if privileged {
		return nil
	}
	// EVERY LEAF THE PATCH TOUCHES, at whatever depth. A flat patch could
	// only name top-level keys, so a nested write was invisible to this
	// check; Keys walks the structure.
	var bad []string
	for _, k := range p.Keys() {
		if systemManaged[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s: written by the harness, not by hand", strings.Join(bad, ", "))
}
