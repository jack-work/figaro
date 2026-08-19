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
	var bad []string
	for k := range p.Set {
		if systemManaged[k] {
			bad = append(bad, k)
		}
	}
	for _, k := range p.Remove {
		if systemManaged[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s: written by the harness, not by hand", strings.Join(bad, ", "))
}
