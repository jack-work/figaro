package boot

import (
	"testing"

	"github.com/jack-work/figaro/internal/config"
)

func TestATurnWithNoCredentialNamesTheFixAndWhetherAnyProviderIsSignedIn(t *testing.T) {
	cfgDir, _ := serverEnv(t)
	loaded := &config.Loaded{ConfigDir: cfgDir}

	if got, want := notSignedIn(loaded, "copilot").Error(),
		"no provider signed in: type :login, or run figaro provider login copilot"; got != want {
		t.Fatalf("nothing signed in:\n got %q\nwant %q", got, want)
	}

	t.Setenv("COPILOT_GITHUB_TOKEN", "gh-own")
	if got, want := notSignedIn(loaded, "copilot").Error(),
		"no provider signed in: type :login, or run figaro provider login copilot"; got != want {
		t.Fatalf("the failing provider's own credential counted as another's:\n got %q\nwant %q", got, want)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-other")
	if got, want := notSignedIn(loaded, "copilot").Error(),
		"copilot is not signed in: type :login, or run figaro provider login copilot"; got != want {
		t.Fatalf("another provider signed in:\n got %q\nwant %q", got, want)
	}
}
