// Package cli: provider wiring for the CLI process.
//
// Provider factories take operational knobs (model, max_tokens,
// reminder_renderer, use_official_sdk) extracted by the angelus
// from the outfit's system.* form keys. Credentials are
// resolved through the auth strategy chain: env var, plaintext
// api_key in providers/<name>.toml, hush-encrypted api_key in
// providers/<name>.toml, OAuth via hush.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"

	// Provider registrations (init side effects).
	_ "github.com/jack-work/figaro/internal/provider/anthropic"
	_ "github.com/jack-work/figaro/internal/provider/copilot"
	_ "github.com/jack-work/figaro/internal/provider/openaichat"
)

// KnownProviders returns the names of all registered providers.
func KnownProviders() []string { return providerPkg.Names() }

// defaultModelFor returns the default model for a provider.
func defaultModelFor(providerName string) string {
	if r := providerPkg.Lookup(providerName); r != nil {
		return r.DefaultModel
	}
	return ""
}

// buildResolver assembles a credential resolver for a provider.
// Walks env -> plaintext api_key -> encrypted api_key -> OAuth.
func buildResolver(loaded *config.Loaded, providerName string) (auth.TokenResolver, error) {
	h := mustHush()
	hushClient := h.Client()

	reg := providerPkg.Lookup(providerName)
	hasOAuth := false
	if reg != nil {
		hasOAuth = reg.HasOAuth
	}

	// A provider whose bearer must be EXCHANGED has exactly one credential
	// source: hush, which performs the exchange and owns the result. The
	// env var and the stored api_key are the durable secret, not a bearer;
	// presenting either to the API would present the wrong credential. They
	// are handed to hush once, here, and never read again.
	if reg != nil && reg.ExchangeGrant != "" && reg.ExchangeURL != nil && reg.ExchangeURL(loaded) != "" {
		if err := ensureExchangeCredential(loaded, hushClient, reg, providerName); err != nil {
			return nil, err
		}
		// Returned bare, not wrapped in an Aggregate: it is the only
		// source, and the provider reads the routing off it directly.
		return &auth.OAuth{Hush: hushClient, Name: providerName}, nil
	}

	strategies := environmentStrategies(reg)
	strategies = append(strategies,
		&auth.ConfigValue{Get: func() string {
			var pa config.ProviderAuth
			_ = loaded.LoadProviderAuth(providerName, &pa)
			if pa.APIKey == "" || strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
				return ""
			}
			return pa.APIKey
		}},
		&encryptedAPIKey{
			Hush:       hushClient,
			ConfigPath: loaded.ProviderAuthPath(providerName),
		},
	)
	if hasOAuth {
		strategies = append(strategies, &auth.OAuth{
			Hush: hushClient,
			Name: providerName,
		})
	}
	return &auth.Aggregate{Strategies: strategies}, nil
}

func environmentStrategies(reg *providerPkg.Registration) []auth.CredentialStrategy {
	if reg == nil {
		return nil
	}
	names := reg.EnvVars
	if len(names) == 0 && reg.EnvVar != "" {
		names = []string{reg.EnvVar}
	}
	strategies := make([]auth.CredentialStrategy, 0, len(names))
	for _, name := range names {
		if name != "" {
			strategies = append(strategies, &auth.EnvVar{Name: name})
		}
	}
	return strategies
}

// providerSetupHint is the user-facing guidance shown when a turn fails
// for lack of a credential.
//
// It answers exactly one question - "figaro has nothing to authenticate
// with" - and must not be shown for any other kind of auth failure. A
// SESSION EXCHANGE that fails (GitHub answering the Copilot token endpoint
// with 403 and an HTML error page) is a different diagnosis with a different
// cure: the credential is present and valid, the derivation hiccuped, and
// retrying is exactly right. Printing this menu there sends the reader to
// re-run a login they already did, and hides the status code that would have
// named the real problem. See sessionExchangeHint.
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

// buildProviderFactory wires per-aria provider construction via the
// registry. No provider-specific switches.
func buildProviderFactory(loaded *config.Loaded, formTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	return func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error) {
		reg := providerPkg.Lookup(providerName)
		if reg == nil {
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
		resolver, err := buildResolver(loaded, providerName)
		if err != nil {
			return nil, err
		}
		cacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
			if backend == nil {
				return nil, fmt.Errorf("no backend")
			}
			return backend.OpenTranslation(aria, providerName)
		}
		return reg.Build(providerPkg.BuildContext{
			Loaded:    loaded,
			Knobs:     knobs,
			Resolver:  resolver,
			Templates: formTmpls,
			CacheOpen: cacheOpen,
			Backend:   backend,
		})
	}
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
	resolver, err := buildResolver(loaded, name)
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
