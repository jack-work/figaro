package copilot

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

// Config is providers/copilot.toml.
type Config struct {
	APIKey           string `toml:"api_key"`
	EnterpriseDomain string `toml:"enterprise_domain,omitempty"`
	TokenMode        string `toml:"token_mode,omitempty"`
	BaseURL          string `toml:"base_url,omitempty"`
}

// exchangeURL returns "" in direct mode: the GitHub token is presented unchanged.
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
		LoginHint:    "Copilot subscription (device code):  figaro provider login copilot",
		Build:        buildFromContext,

		// hush exchanges the GitHub token for a short-lived session token,
		// so one machine holds one session.
		ExchangeGrant: hush.GrantCopilot,
		ExchangeURL:   exchangeURL,
	})
}

func buildFromContext(ctx provider.BuildContext) (provider.Provider, error) {
	if ctx.Resolver == nil {
		return nil, fmt.Errorf("copilot: nil token resolver (need GitHub access token)")
	}
	cfg := loadConfig(ctx.Loaded)
	tokens := newTokenSource(ctx.Resolver, cfg)
	file, err := routed.LoadFile(ctx.Loaded, providerName)
	if err != nil {
		return nil, err
	}
	file.BaseURL = ""
	spec, err := file.Apply(Spec(tokens))
	if err != nil {
		return nil, err
	}
	ctx.Resolver = tokens
	return routed.Build(ctx, spec, "")
}
