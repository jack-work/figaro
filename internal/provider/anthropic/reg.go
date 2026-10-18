package anthropic

import (
	"encoding/json"

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
	return a, nil
}
