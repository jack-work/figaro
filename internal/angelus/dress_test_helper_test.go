package angelus_test

import "testing"

// dress is the outfit names a test sends; the daemon resolves them at the boundary.
func dress(t testing.TB, names ...string) []string {
	t.Helper()
	return names
}
