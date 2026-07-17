package copilot

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Config is the copilot-specific provider config (providers/copilot.toml).
// The copilot package owns its deserialization.
type Config struct {
	APIKey           string `toml:"api_key"`
	EnterpriseDomain string `toml:"enterprise_domain,omitempty"`
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
		if ctx.Backend == nil {
			return nil, fmt.Errorf("no backend")
		}
		return ctx.Backend.OpenTranslation(aria, "copilot-messages")
	}
	responsesCacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
		if ctx.Backend == nil {
			return nil, fmt.Errorf("no backend")
		}
		return ctx.Backend.OpenTranslation(aria, "copilot-responses")
	}
	p, err := New(knobs, ctx.Resolver, cfg.EnterpriseDomain, messagesCacheOpen, responsesCacheOpen)
	if err != nil {
		return nil, err
	}
	p.SetTemplates(ctx.Templates)
	return p, nil
}
