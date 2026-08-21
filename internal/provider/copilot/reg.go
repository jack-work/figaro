package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Config is the copilot-specific provider config (providers/copilot.toml).
// The copilot package owns its deserialization.
type Config struct {
	APIKey           string `toml:"api_key"`
	EnterpriseDomain string `toml:"enterprise_domain,omitempty"`
	TokenMode        string `toml:"token_mode,omitempty"`
	BaseURL          string `toml:"base_url,omitempty"`
}

// exchangeURL is where hush mints the session token. Direct mode returns
// "": that installation presents the GitHub token unchanged, as the Copilot
// CLI does, and there is nothing to exchange.
func exchangeURL(loaded *config.Loaded) string {
	cfg := loadConfig(loaded)
	if strings.EqualFold(strings.TrimSpace(cfg.TokenMode), "direct") {
		return ""
	}
	domain := strings.TrimSpace(cfg.EnterpriseDomain)
	if domain == "" {
		domain = "github.com"
	}
	return fmt.Sprintf("https://api.%s/copilot_internal/v2/token", domain)
}

func loadConfig(loaded *config.Loaded) Config {
	var cfg Config
	path := loaded.ProviderAuthPath("copilot")
	if data, err := os.ReadFile(path); err == nil {
		toml.Unmarshal(data, &cfg)
	}
	return cfg
}

func init() {
	provider.Register(&provider.Registration{
		Name:         "copilot",
		DefaultModel: "claude-sonnet-4.5",
		EnvVar:       "COPILOT_GITHUB_TOKEN",
		EnvVars:      []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
		HasOAuth:     true,
		LoginHint:    "Copilot subscription (device code):  figaro login copilot",
		Build:        buildFromContext,

		// The Copilot API will not take a GitHub token: it must be
		// exchanged for a session token that lives about half an hour.
		// hush performs and owns that exchange (grant "copilot"), so
		// one machine holds one session no matter how many arias run.
		ExchangeGrant: hush.GrantCopilot,
		ExchangeURL:   exchangeURL,
	})
}

func buildFromContext(ctx provider.BuildContext) (provider.Provider, error) {
	knobs := ctx.Knobs
	if knobs.MaxTokens == 0 {
		knobs.MaxTokens = 16000
	}
	reg := provider.Lookup("copilot")
	if knobs.Model == "" && reg != nil {
		knobs.Model = reg.DefaultModel
	}
	cfg := loadConfig(ctx.Loaded)
	messagesCacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
		return ctx.Backend.OpenTranslator(aria, "copilot-messages")
	}
	responsesCacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
		return ctx.Backend.OpenTranslator(aria, "copilot-responses")
	}
	p, err := New(knobs, ctx.Resolver, cfg, messagesCacheOpen, responsesCacheOpen)
	if err != nil {
		return nil, err
	}
	p.SetTemplates(ctx.Templates)
	return p, nil
}
