package meta

import (
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

const providerName = "meta"

func init() {
	provider.Register(&provider.Registration{
		Name:         providerName,
		DefaultModel: "muse-wonder-1.0",
		EnvVar:       "META_API_KEY",
		LoginHint:    "Muse Code subscription (device code):  figaro provider login meta",
		Build:        build,
	})
}

func Spec() routed.Spec {
	base := auth.MetaAPIBase + "/v1"
	return routed.Spec{
		Name:         providerName,
		BaseURL:      func() string { return base },
		Catalog:      "/models",
		Default:      routed.Responses,
		MessagesPath: "/messages",
	}
}

func build(ctx provider.BuildContext) (provider.Provider, error) {
	file, err := routed.LoadFile(ctx.Loaded, providerName)
	if err != nil {
		return nil, err
	}
	spec, err := file.Apply(Spec())
	if err != nil {
		return nil, err
	}
	return routed.Build(ctx, spec, file.APIKey)
}
