package auth

import (
	"os"
	"runtime"
	"testing"

	"github.com/jack-work/figaro/internal/testfence"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	testfence.Install()
	os.Exit(m.Run())
}

// A login under test must never open the user's real browser: go test ./...
// ran the device-flow tests through xdg-open, and a sign-in page landed on
// Gluck's desktop.
func TestATestNeverReachesTheRealBrowser(t *testing.T) {
	cmds := browserCommands(os.Getenv("BROWSER"), runtime.GOOS, "https://x/device")
	assert.Equal(t, []string{testfence.NoBrowser, "https://x/device"}, cmds[0])
}
