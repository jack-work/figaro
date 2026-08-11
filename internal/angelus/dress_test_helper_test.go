package angelus_test

import "testing"

// dress is the outfit axis a test means when it says "on outfit mock": NAMES,
// which the daemon's one dressing call resolves at the API boundary. Nothing a
// test sends carries a directive inside a patch any more.
func dress(t testing.TB, names ...string) []string {
	t.Helper()
	return names
}
