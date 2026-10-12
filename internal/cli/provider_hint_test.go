package cli

import (
	"strings"
	"testing"
)

// The bug this splits: a 403 from GitHub's Copilot token-exchange endpoint
// arrived as "resolve token: copilot token exchange 403: <html>", figaro
// flattened it into the setup menu, and the reader was told to connect a
// provider that was already connected - with the status code discarded.
func TestAuthFailureHintSeparatesMissingFromUnexchangeable(t *testing.T) {
	setup, ok := authFailureHint("provider \"copilot\": no credential available (no strategy returned a token)")
	if !ok {
		t.Fatal("a missing credential must still be explained")
	}
	if !contains(setup, "No provider connected") {
		t.Fatalf("missing-credential hint lost its menu:\n%s", setup)
	}

	reason := "copilot models: resolve token: copilot token exchange 403: <!DOCTYPE html>"
	exch, ok := authFailureHint(reason)
	if !ok {
		t.Fatal("a failed exchange must be explained")
	}
	if contains(exch, "No provider connected") {
		t.Fatalf("a failed exchange was reported as a missing credential:\n%s", exch)
	}
	if !contains(exch, "403") {
		t.Fatalf("the provider's own status code was discarded:\n%s", exch)
	}
	if !contains(exch, "retry") {
		t.Fatalf("the transient case must say what to do:\n%s", exch)
	}

	if _, ok := authFailureHint("context deadline exceeded"); ok {
		t.Fatal("an unrelated failure was claimed as an auth diagnosis")
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// THE MESSAGE GLUCK GOT, verbatim: the vault agent's socket was missing, and
// the hint blamed a rate-limited exchange endpoint for a credential that never
// left the vault. It names the vault now, as does a failed revival.
func TestAuthFailureHintNamesTheVaultWhenTheAgentIsGone(t *testing.T) {
	for _, reason := range []string{
		"copilot: resolve token: dial unix /tmp/figaro-hush/agent.sock: connect: no such file or directory",
		"resolve token: no credential available; first strategy error: dial unix /tmp/figaro-hush/agent.sock: connect: connection refused",
		"copilot: resolve token: dial unix /tmp/figaro-hush/agent.sock: connect: no such file or directory\nhush agent would not start: unlock figaro: keyring locked",
	} {
		hint, ok := authFailureHint(reason)
		if !ok {
			t.Fatalf("no hint for %q", reason)
		}
		if strings.Contains(hint, "rate-limits") || strings.Contains(hint, "exchanging it for a session token failed") {
			t.Fatalf("the vault agent's absence was blamed on the exchange endpoint:\n%s", hint)
		}
		if !strings.Contains(hint, "figaro vault status") {
			t.Fatalf("the hint does not point at the vault:\n%s", hint)
		}
	}
	// And a genuine exchange failure keeps the exchange hint.
	hint, _ := authFailureHint("copilot: resolve token: token exchange: 429 Too Many Requests")
	if !strings.Contains(hint, "exchanging it for a session token failed") {
		t.Fatalf("an exchange failure lost its hint:\n%s", hint)
	}
}
