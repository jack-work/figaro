package form

// Protection: which keys a caller off the wire may write.
//
// The mode has been in WellKnownKeys since it was written and has never been
// enforced. This is the enforcement, and it is deliberately only half of
// phase 4: it answers "may THIS caller write THIS key", not "is this value
// the right shape". Shape validation wants a per-key validator and a
// provider-keyed system schema, and both want this to exist first.
//
// Privilege is a property of the CALL SITE, not a field on a request. There
// is no JSON tag anywhere near it: an in-process caller passes true, and
// anything that arrived over a socket cannot.

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
//
// It applies to what a patch WRITES, never to what a board already HOLDS:
// otherwise the first aria carrying a stray key becomes unpatchable, which
// is a worse failure than the one being prevented.
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
