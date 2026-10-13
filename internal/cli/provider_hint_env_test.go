package cli

import (
	"strings"
	"testing"

	providerPkg "github.com/jack-work/figaro/internal/provider"
)

// A provider that declares only the plural EnvVars must still be connectable
// from the hint: the hint printed the singular field and nothing else, so such
// a provider appeared in the list with no instruction under it.
func TestProviderSetupHintNamesEveryAcceptedVariable(t *testing.T) {
	providerPkg.Register(&providerPkg.Registration{
		Name:    "hint-plural",
		EnvVars: []string{"HINT_FIRST_KEY", "HINT_SECOND_KEY"},
	})
	hint := providerSetupHint()
	for _, env := range []string{"HINT_FIRST_KEY", "HINT_SECOND_KEY"} {
		if !strings.Contains(hint, env) {
			t.Fatalf("%s is missing from the hint:\n%s", env, hint)
		}
	}
}
