package cli

import "testing"

// The pager refuses every verb in a replay before it dials; these are the
// endpoints behind it. A path the pager missed must stop here, loudly, rather
// than reach the daemon, spawn one, or open the vault.
func TestAReplayDiesAtEveryEndpointOffItsTape(t *testing.T) {
	tapeOnly.Store(true)
	defer tapeOnly.Store(false)
	for name, reach := range map[string]func(){
		"resolveDoor":   func() { resolveEndpoint() },
		"ensureAngelus": ensureAngelus,
		"MustHush":      func() { MustHush() },
		"cliOAuthStore": func() { _, _ = cliOAuthStore(false) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if _, ok := recover().(exitPanic); !ok {
					t.Fatalf("%s went on past a replay's tape", name)
				}
			}()
			reach()
		})
	}
}
