package boot

import (
	"errors"
	"testing"
)

// An isolated vault (FIGARO_HUSH_DIR) under the default app name saves its
// passphrase to the keyring entry figaro:default, overwriting the user's.
func TestHushAppName_RefusesAnIsolatedVaultUnderTheDefaultName(t *testing.T) {
	t.Setenv("FIGARO_HUSH_APP", "")
	t.Setenv("FIGARO_HUSH_DIR", t.TempDir())
	if _, err := HushAppName(); !errors.Is(err, errHushDirWithoutApp) {
		t.Fatalf("HushAppName() err = %v, want errHushDirWithoutApp", err)
	}
	resetHush()
	t.Cleanup(resetHush)
	if _, err := Hush(); !errors.Is(err, errHushDirWithoutApp) {
		t.Fatalf("Hush() err = %v, want errHushDirWithoutApp", err)
	}
}

func TestHushAppName(t *testing.T) {
	for _, tc := range []struct{ app, dir, want string }{
		{"", "", "figaro"},
		{"figaro-dev-x", "", "figaro-dev-x"},
		{"figaro-dev-x", "/tmp/somewhere", "figaro-dev-x"},
	} {
		t.Setenv("FIGARO_HUSH_APP", tc.app)
		t.Setenv("FIGARO_HUSH_DIR", tc.dir)
		got, err := HushAppName()
		if err != nil || got != tc.want {
			t.Errorf("app=%q dir=%q: got %q, %v; want %q", tc.app, tc.dir, got, err, tc.want)
		}
	}
}
