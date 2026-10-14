package routed

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

const PresetGateway = "gateway"

var DeclaredLogin func(loaded *config.Loaded, name string, oauth auth.OAuthSettings) error

func RegisterDeclared(loaded *config.Loaded) []string {
	if loaded == nil {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(loaded.ProvidersDir(), "*.toml"))
	sort.Strings(paths)
	var names []string
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".toml")
		reg, err := declared(loaded, name)
		if err != nil {
			slog.Warn("provider file ignored", "path", path, "err", err)
			continue
		}
		if reg == nil {
			continue
		}
		provider.Register(reg)
		names = append(names, name)
	}
	return names
}

func declared(loaded *config.Loaded, name string) (*provider.Registration, error) {
	f, err := LoadFile(loaded, name)
	if err != nil {
		return nil, err
	}
	if f.Preset == "" {
		return nil, nil
	}
	if existing := provider.Lookup(name); existing != nil && !existing.Declared {
		return nil, fmt.Errorf("%q is a built-in provider; preset applies only to a new name", name)
	}
	if f.Preset != PresetGateway {
		return nil, fmt.Errorf("preset %q is not one of: %s", f.Preset, PresetGateway)
	}
	reg := &provider.Registration{
		Name:     name,
		Declared: true,
		HasOAuth: f.OAuth != nil,
		Build: func(ctx provider.BuildContext) (provider.Provider, error) {
			return buildGatewayAs(ctx, name, name)
		},
	}
	if f.OAuth != nil {
		reg.LoginHint = "OAuth gateway (providers/" + name + ".toml):  figaro login " + name
		login := func(l *config.Loaded) error {
			if DeclaredLogin == nil {
				return fmt.Errorf("%s: no login flow in this binary", name)
			}
			f, err := LoadFile(l, name)
			if err != nil {
				return err
			}
			if f.OAuth == nil {
				return fmt.Errorf("%s: providers/%s.toml has no [oauth] section", name, name)
			}
			return DeclaredLogin(l, name, *f.OAuth)
		}
		reg.Login = login
		reg.Setup = login
	}
	return reg, nil
}
