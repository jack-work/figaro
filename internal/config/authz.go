package config

import (
	"errors"
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
}

type APIKeyConfig struct {
	ID        string    `toml:"id"`
	Principal string    `toml:"principal"`
	SHA256    string    `toml:"sha256"`
	NotAfter  time.Time `toml:"not_after"`
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
