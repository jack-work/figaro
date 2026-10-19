package boot

import (
	"errors"
	"testing"
)

// The embedded hush agent is started by re-executing os.Executable(). In a
// test that is the test binary, so a spawn reruns the suite, which spawns
// again: a fork bomb that took a 16G scope down in 15 s. The guard must
// refuse before any process starts.
func TestHush_RefusesToStartAnAgentFromATestBinary(t *testing.T) {
	resetHush()
	t.Cleanup(resetHush)
	t.Setenv("FIGARO_HUSH_APP", "figaro-test-spawnguard")
	t.Setenv("FIGARO_HUSH_DIR", t.TempDir())
	t.Setenv("FIGARO_HUSH_PASSPHRASE", "spawnguard")
	h, err := Hush()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.EnsureReady(); !errors.Is(err, ErrHushAgentFromTestBinary) {
		t.Fatalf("EnsureReady() err = %v, want ErrHushAgentFromTestBinary", err)
	}
}

func TestIsTestBinary(t *testing.T) {
	for exe, want := range map[string]bool{
		"/tmp/go-build1/b001/boot.test":       true,
		`C:\tmp\boot.test.exe`:                true,
		"/home/gluck/.nix-profile/bin/figaro": false,
		"/tmp/figaro-test":                    false,
	} {
		if got := IsTestBinary(exe); got != want {
			t.Errorf("IsTestBinary(%q) = %v, want %v", exe, got, want)
		}
	}
}
