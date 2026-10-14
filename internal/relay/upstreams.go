package relay

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

func Upstreams(loaded *config.Loaded, names []string) (map[string]Upstream, map[string]string) {
	if len(names) == 0 {
		names = provider.Names()
	}
	ups := map[string]Upstream{}
	skipped := map[string]string{}
	for _, name := range names {
		up, err := Resolve(loaded, name)
		if err != nil {
			skipped[name] = err.Error()
			continue
		}
		ups[name] = up
	}
	return ups, skipped
}

func Resolve(loaded *config.Loaded, name string) (Upstream, error) {
	reg := provider.Lookup(name)
	if reg == nil {
		return nil, fmt.Errorf("no such provider (have: %s)", strings.Join(provider.Names(), ", "))
	}
	resolver, err := boot.BuildResolver(loaded, name)
	if err != nil {
		return nil, err
	}
	p, err := reg.Build(provider.BuildContext{
		Loaded:   loaded,
		Knobs:    provider.Knobs{Model: reg.DefaultModel, MaxTokens: 8192},
		Resolver: resolver,
	})
	if err != nil {
		return nil, err
	}
	up, ok := p.(Upstream)
	if !ok {
		return nil, fmt.Errorf("%T speaks figaro's IR only; it has no upstream to relay to", p)
	}
	if _, err := up.Credential().Resolve(); err != nil {
		return nil, err
	}
	return up, nil
}
