package form

import "testing"

// Defends the role keys' classes: as self-class keys a write:aria:<role>#@self
// grant would reach them, and target-aria is the seat while
// succession-policy picks the outfit the daemon dresses a successor in.
func TestRoleKeysAreNotSelfClass(t *testing.T) {
	for path, want := range map[string]Writer{
		"target-aria":              WriterLineage,
		"succession-policy":        WriterOwner,
		"succession-policy.outfit": WriterOwner,
	} {
		if got := WriterOf(path); got != want {
			t.Errorf("%s is %s class, want %s", path, got, want)
		}
	}
}
