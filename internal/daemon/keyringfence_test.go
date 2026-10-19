package daemon

import (
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/keyringfence"
)

// No test may reach the OS keyring: a test vault that saves its passphrase
// there can overwrite the user's real entry (figaro:default).
func TestMain(m *testing.M) {
	keyringfence.Install()
	os.Exit(m.Run())
}
