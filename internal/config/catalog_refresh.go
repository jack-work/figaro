package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DefaultCatalogRefresh = 24 * time.Hour
	MinCatalogRefresh     = time.Minute
)

func parseCatalogRefresh(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultCatalogRefresh, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("catalog_refresh %q is not a duration such as \"15m\" or \"24h\"", raw)
	}
	if d < MinCatalogRefresh {
		return 0, fmt.Errorf("catalog_refresh %q is below the minimum of %s", raw, MinCatalogRefresh)
	}
	return d, nil
}

func (l *Loaded) CatalogRefreshOf(provider string) (time.Duration, error) {
	if l == nil || provider == "" {
		return DefaultCatalogRefresh, nil
	}
	var file struct {
		CatalogRefresh string `toml:"catalog_refresh"`
	}
	if err := l.LoadProviderAuth(provider, &file); err != nil {
		return 0, err
	}
	d, err := parseCatalogRefresh(file.CatalogRefresh)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", l.ProviderAuthPath(provider), err)
	}
	return d, nil
}

func (l *Loaded) CatalogRefresh(provider string) time.Duration {
	d, err := l.CatalogRefreshOf(provider)
	if err != nil {
		return DefaultCatalogRefresh
	}
	return d
}

func (l *Loaded) validateCatalogRefresh() error {
	for _, name := range l.ListProviders() {
		if _, err := l.CatalogRefreshOf(name); err != nil {
			return err
		}
	}
	return nil
}

func refuseModelsPoll(md toml.MetaData) error {
	if md.IsDefined("models", "poll_minutes") {
		return fmt.Errorf("[models] poll_minutes is gone; set catalog_refresh (for example \"6h\") in each provider file instead")
	}
	return nil
}
