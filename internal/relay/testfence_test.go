package relay

import (
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/testfence"
)

// No test may reach the OS keyring (a test vault that saves its passphrase
// there can overwrite the user's real entry, figaro:default), and none may
// inherit a dev shell's vault, whose agent would re-execute the test binary.
func TestMain(m *testing.M) {
	testfence.Install()
	os.Exit(m.Run())
}
