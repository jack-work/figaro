package anthropic

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/catalog"
	"github.com/jack-work/figaro/internal/catalogmaps"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func init() {
	provider.Register(&provider.Registration{
		Name:         "anthropic",
		DefaultModel: "claude-sonnet-4-5",
		EnvVar:       "ANTHROPIC_API_KEY",
		HasOAuth:     true,
		LoginHint:    "Claude subscription (OAuth):  figaro provider login anthropic",
		Build:        buildFromContext,
	})
}

func buildFromContext(ctx provider.BuildContext) (provider.Provider, error) {
	knobs := ctx.Knobs
	if knobs.MaxTokens == 0 {
		knobs.MaxTokens = 8192
	}
	reg := provider.Lookup("anthropic")
	if knobs.Model == "" && reg != nil {
		knobs.Model = reg.DefaultModel
	}
	cacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
		return ctx.Backend.OpenTranslator(aria, "anthropic")
	}
	a, err := New(knobs, ctx.Resolver, cacheOpen)
	if err != nil {
		return nil, err
	}
	a.Templates = ctx.Templates
	data, err := readProviderFile(ctx.Loaded)
	if err != nil {
		return nil, err
	}
	if a.Catalog, err = catalogFrom(data); err != nil {
		return nil, err
	}
	if a.SendFigaroID, err = sendsFigaroID(data); err != nil {
		return nil, err
	}
	return a, nil
}

func readProviderFile(loaded *config.Loaded) ([]byte, error) {
	if loaded == nil {
		return nil, nil
	}
	path := loaded.ProviderAuthPath(providerName)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return data, nil
}

func sendsFigaroID(data []byte) (bool, error) {
	var f struct {
		SendFigaroID bool `toml:"send_figaro_id"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return false, fmt.Errorf("anthropic: %w", err)
	}
	return f.SendFigaroID, nil
}

func catalogFrom(data []byte) (*catalog.Reader, error) {
	file, err := catalog.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	r, err := catalog.New(catalogmaps.Builtin(providerName).With(file.Catalog), file.Models)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return r, nil
}
