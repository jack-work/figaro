package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type AuthzConfig struct {
	Policy        string         `toml:"policy"`
	TroupeIssuers *bool          `toml:"troupe_issuers"`
	Peercred      PeercredConfig `toml:"peercred"`
	Upstream      UpstreamConfig `toml:"upstream"`
	Issuers       []IssuerConfig `toml:"issuers"`
	APIKeys       []APIKeyConfig `toml:"api_keys"`
	ProtectedKeys string         `toml:"protected_keys"`
}

type APIKeyConfig struct {
	ID        string    `toml:"id"`
	Principal string    `toml:"principal"`
	SHA256    string    `toml:"sha256"`
	NotAfter  time.Time `toml:"not_after"`
	Nodes     []string  `toml:"nodes"`
}

type PeercredConfig struct {
	UIDs map[string]string `toml:"uids"`
}

type UpstreamConfig struct {
	Issuer   string            `toml:"issuer"`
	Services map[string]string `toml:"services"`
	Reverify bool              `toml:"reverify"`
}

type IssuerConfig struct {
	Issuer   string            `toml:"issuer"`
	JWKS     string            `toml:"jwks"`
	Userinfo string            `toml:"userinfo"`
	Audience string            `toml:"audience"`
	Types    []string          `toml:"types"`
	Users    []string          `toml:"users"`
	Clients  map[string]string `toml:"clients"`
}

var errRetiredBearer = errors.New("[authz.bearer] is now [[authz.issuers]]: same keys, one table per issuer, and principals carry the issuer (user:jack@auth.kelliher.info)")

const (
	ProtectedByLineage = "lineage"
	ProtectedOpen      = "open"
)

func (c AuthzConfig) ProtectsByLineage() bool { return c.ProtectedKeys != ProtectedOpen }

func (c AuthzConfig) checkProtectedKeys() error {
	switch c.ProtectedKeys {
	case "", ProtectedByLineage, ProtectedOpen:
		return nil
	}
	return fmt.Errorf("[authz] protected_keys = %q: want %q or %q", c.ProtectedKeys, ProtectedByLineage, ProtectedOpen)
}

func (c AuthzConfig) TakesTroupeIssuers() bool {
	return c.TroupeIssuers == nil || *c.TroupeIssuers
}

func (l *Loaded) PolicyPath() string {
	p := l.cfg().Authz.Policy
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.dir(), p)
}
