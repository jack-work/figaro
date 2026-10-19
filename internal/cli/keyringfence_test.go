package cli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/keyringfence"
)

// Defends the fence TestMain installs: a test run, and every daemon, hush
// agent or CLI it spawns, must not reach the OS keyring holding figaro:default.
func TestKeyringFence_TestProcessCannotReachTheOSKeyring(t *testing.T) {
	if err := keyringfence.Sealed(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringFence_SpawnedProcessesInheritTheDeadBus(t *testing.T) {
	out, err := exec.Command("sh", "-c", `printf %s "$DBUS_SESSION_BUS_ADDRESS"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != keyringfence.DeadSessionBus {
		t.Fatalf("child sees DBUS_SESSION_BUS_ADDRESS=%q, want %q", got, keyringfence.DeadSessionBus)
	}
}
