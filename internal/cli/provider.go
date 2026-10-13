package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

// defaultModelFor returns the default model for a provider.
func defaultModelFor(providerName string) string {
	if r := providerPkg.Lookup(providerName); r != nil {
		return r.DefaultModel
	}
	return ""
}

// providerSetupHint is the user-facing guidance shown when a turn fails
// for lack of a credential.
func providerSetupHint() string {
	var b strings.Builder
	b.WriteString("No provider connected: figaro has no credential to reach a model.\n\n")
	b.WriteString("Connect one and retry:\n")
	for _, name := range providerPkg.Names() {
		reg := providerPkg.Lookup(name)
		if reg == nil {
			continue
		}
		if reg.HasOAuth && reg.LoginHint != "" {
			fmt.Fprintf(&b, "  • %-10s %s\n", name, reg.LoginHint)
		}
		if reg.EnvVar != "" {
			fmt.Fprintf(&b, "  • %-10s credential:                   export %s=…\n", name, reg.EnvVar)
		}
	}
	return b.String()
}

// sessionExchangeHint explains a failure to DERIVE a session token from a
// credential figaro does hold, and keeps the provider's own words: the
// status code and body are the whole diagnosis.
func sessionExchangeHint(reason string) string {
	var b strings.Builder
	b.WriteString(reason)
	if !strings.HasSuffix(reason, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\nfigaro HAS a credential; exchanging it for a session token failed.\n")
	b.WriteString("This is usually transient (the exchange endpoint rate-limits bursts):\n")
	b.WriteString("retry the turn. If it persists, the stored credential may be revoked:\n")
	b.WriteString("  figaro doctor provider   (session credentials, exchanges, expiry)\n")
	return b.String()
}

// authFailureHint picks the right explanation for a failed turn: no
// credential at all, or a credential that could not be exchanged.
func authFailureHint(reason string) (string, bool) {
	switch {
	case strings.Contains(reason, "resolve token"), strings.Contains(reason, "token exchange"):
		return sessionExchangeHint(reason), true
	case strings.Contains(reason, "no credential"):
		return providerSetupHint(), true
	}
	return "", false
}

// buildProvider constructs a one-off provider for read-only flows
// (e.g. `figaro models`).
func buildProvider(loaded *config.Loaded, name string) (providerPkg.Provider, int) {
	reg := providerPkg.Lookup(name)
	if reg == nil {
		return nil, 0
	}
	// The registry's own defaults: this is a read-only flow that needs a model
	// to authenticate with, not the model an aria would use. Reading that off
	// the default outfit meant a client folding server state to list models.
	knobs := providerPkg.Knobs{Model: reg.DefaultModel, MaxTokens: 8192}
	resolver, err := boot.BuildResolver(loaded, name)
	if err != nil {
		return nil, 0
	}
	p, err := reg.Build(providerPkg.BuildContext{
		Loaded:   loaded,
		Knobs:    knobs,
		Resolver: resolver,
	})
	if err != nil {
		return nil, 0
	}
	return p, knobs.MaxTokens
}
