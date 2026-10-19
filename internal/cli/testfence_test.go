package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/testfence"
)

// Defends the fence TestMain installs: a test run, and every daemon, hush
// agent or CLI it spawns, must not reach the OS keyring holding figaro:default.
func TestKeyringFence_TestProcessCannotReachTheOSKeyring(t *testing.T) {
	if err := testfence.Sealed(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringFence_SpawnedProcessesInheritTheDeadBus(t *testing.T) {
	out, err := exec.Command("sh", "-c", `printf %s "$DBUS_SESSION_BUS_ADDRESS"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != testfence.DeadSessionBus {
		t.Fatalf("child sees DBUS_SESSION_BUS_ADDRESS=%q, want %q", got, testfence.DeadSessionBus)
	}
}

// A dev shell exports an isolated vault (FIGARO_HUSH_DIR, and a passphrase
// that unlocks it without a prompt). A test that inherits both can start an
// embedded hush agent, which re-executes the test binary.
func TestTestFence_TestsInheritNoVault(t *testing.T) {
	for _, k := range []string{"FIGARO_HUSH_DIR", "FIGARO_HUSH_PASSPHRASE"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%q reached the test process", k, v)
		}
	}
}
