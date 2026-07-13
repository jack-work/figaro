package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropicsdk"
	"github.com/jack-work/figaro/internal/store"
)

func init() {
	provider.Register(&provider.Registration{
		Name:         "anthropic",
		DefaultModel: "claude-sonnet-4-5",
		EnvVar:       "ANTHROPIC_API_KEY",
		HasOAuth:     true,
		LoginHint:    "Claude subscription (OAuth):  figaro login anthropic",
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
		if ctx.Backend == nil {
			return nil, fmt.Errorf("no backend")
		}
		return ctx.Backend.OpenTranslation(aria, "anthropic")
	}
	if knobs.UseOfficialSDK {
		p, err := anthropicsdk.New(knobs, ctx.Resolver, cacheOpen)
		if err != nil {
			return nil, err
		}
		p.Templates = ctx.Templates
		return p, nil
	}
	a, err := New(knobs, ctx.Resolver, cacheOpen)
	if err != nil {
		return nil, err
	}
	a.Templates = ctx.Templates
	return a, nil
}
