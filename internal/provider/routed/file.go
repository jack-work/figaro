package routed

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

type File struct {
	Preset           string                       `toml:"preset,omitempty"`
	OAuth            *auth.OAuthSettings          `toml:"oauth,omitempty"`
	APIKey           string                       `toml:"api_key"`
	BaseURL          string                       `toml:"base_url,omitempty"`
	CacheMarkers     string                       `toml:"cache_markers,omitempty"`
	MessagesPath     string                       `toml:"messages_path,omitempty"`
	Headers          map[string]string            `toml:"headers,omitempty"`
	TransportHeaders map[string]map[string]string `toml:"transport_headers,omitempty"`
	Routing          struct {
		Catalog string `toml:"catalog,omitempty"`
		Default string `toml:"default,omitempty"`
		Rules   []struct {
			Model     string `toml:"model"`
			Transport string `toml:"transport"`
		} `toml:"rule,omitempty"`
	} `toml:"routing"`
}

func LoadFile(loaded *config.Loaded, name string) (File, error) {
	var f File
	if loaded == nil {
		return f, nil
	}
	path := loaded.ProviderAuthPath(name)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f File) Apply(spec Spec) (Spec, error) {
	if b := strings.TrimRight(strings.TrimSpace(f.BaseURL), "/"); b != "" {
		spec.BaseURL = func() string { return b }
	}
	if f.MessagesPath != "" {
		spec.MessagesPath = f.MessagesPath
	}
	if len(f.Headers) > 0 {
		spec.Headers = merge(spec.Headers, f.Headers)
	}
	for name, h := range f.TransportHeaders {
		t, err := ParseTransport(name)
		if err != nil || t == "" {
			return spec, fmt.Errorf("%s: transport_headers.%s: %v", spec.Name, name, err)
		}
		if spec.TransportHeaders == nil {
			spec.TransportHeaders = map[Transport]map[string]string{}
		}
		spec.TransportHeaders[t] = merge(spec.TransportHeaders[t], h)
	}
	if f.Routing.Catalog != "" {
		spec.Catalog = "/" + strings.TrimLeft(f.Routing.Catalog, "/")
	}
	if f.Routing.Default != "" {
		t, err := ParseTransport(f.Routing.Default)
		if err != nil {
			return spec, fmt.Errorf("%s: routing.default: %w", spec.Name, err)
		}
		spec.Default = t
	}
	if len(f.Routing.Rules) > 0 {
		rules := make([]Rule, 0, len(f.Routing.Rules))
		for _, r := range f.Routing.Rules {
			t, err := ParseTransport(r.Transport)
			if err != nil {
				return spec, fmt.Errorf("%s: routing.rule %q: %w", spec.Name, r.Model, err)
			}
			rules = append(rules, Rule{Model: r.Model, Transport: t})
		}
		spec.Rules = append(rules, spec.Rules...)
	}
	if mode := strings.TrimSpace(f.CacheMarkers); mode != "" && !strings.EqualFold(mode, "trusted") {
		spec.MarkMode = ParseMarkMode(mode)
	}
	return spec, spec.Validate()
}

func ParseMarkMode(s string) provider.MarkMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocks", "block", "explicit":
		return provider.MarkBlocks
	case "top-level", "toplevel", "automatic":
		return provider.MarkTopLevel
	case "none", "off":
		return provider.MarkNone
	}
	return provider.MarkAuto
}

func merge(base, over map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}
