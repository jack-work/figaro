package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

// Some providers will not accept the credential a human owns. GitHub Copilot
// is the standing example: the API refuses a GitHub token outright, and
// wants a session token exchanged from it, good for about half an hour.

// exchangeSetupMu serializes credential bootstrap within this process.
var exchangeSetupMu sync.Mutex

// ensureExchangeCredential registers the provider's durable secret with hush
// under an exchange grant, if hush does not already hold it. Idempotent and
// cheap: the common path is one oauth_list over a unix socket.
func ensureExchangeCredential(loaded *config.Loaded, h *hush.Client, reg *providerPkg.Registration, name string) error {
	exchangeSetupMu.Lock()
	defer exchangeSetupMu.Unlock()

	if h == nil || reg == nil || reg.ExchangeGrant == "" {
		return nil
	}
	tokenURL := ""
	if reg.ExchangeURL != nil {
		tokenURL = reg.ExchangeURL(loaded)
	}
	if tokenURL == "" {
		// This installation does not exchange (Copilot's direct mode).
		return nil
	}

	// The agent may predate this figaro: it is figaro's own re-exec and
	// outlives the binary that spawned it. hush knows how to replace its
	// own child.
	if err := mustHush().EnsureGrant(reg.ExchangeGrant); err != nil {
		return err
	}

	names, err := h.OAuthList()
	if err != nil {
		return fmt.Errorf("hush: list credentials: %w", err)
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}

	durable, source := durableSecretFor(loaded, h, reg, name)
	if durable == "" {
		// Nothing to hand over. The turn will fail with the ordinary
		// no-credential hint, which is the truth.
		return nil
	}

	if err := h.OAuthRegister(hush.OAuthRegisterRequest{
		Name:         name,
		TokenURL:     tokenURL,
		Grant:        reg.ExchangeGrant,
		RefreshToken: durable,
	}); err != nil {
		return fmt.Errorf("hush: register %s credential from %s: %w", name, source, err)
	}
	fmt.Fprintf(stderrw, "figaro: handed the %s credential (%s) to hush; it owns the session token now\n", name, source)
	return nil
}

// durableSecretFor finds the long-lived secret to hand hush, and names where
// it came from so the migration is not silent.
func durableSecretFor(loaded *config.Loaded, h *hush.Client, reg *providerPkg.Registration, name string) (secret, source string) {
	envNames := reg.EnvVars
	if len(envNames) == 0 && reg.EnvVar != "" {
		envNames = []string{reg.EnvVar}
	}
	for _, env := range envNames {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, "$" + env
		}
	}

	var pa config.ProviderAuth
	if err := loaded.LoadProviderAuth(name, &pa); err != nil || pa.APIKey == "" {
		return "", ""
	}
	path := loaded.ProviderAuthPath(name)
	if !strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
		return pa.APIKey, path
	}
	dec, err := h.Decrypt(map[string]string{"api_key": pa.APIKey})
	if err != nil {
		return "", ""
	}
	return dec["api_key"], path
}
