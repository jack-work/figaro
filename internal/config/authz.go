package config

import "path/filepath"

type AuthzConfig struct {
	Policy   string         `toml:"policy"`
	Peercred PeercredConfig `toml:"peercred"`
	Upstream UpstreamConfig `toml:"upstream"`
	Bearer   *BearerConfig  `toml:"bearer"`
}

type PeercredConfig struct {
	UIDs map[string]string `toml:"uids"`
}

type UpstreamConfig struct {
	Listen       []string `toml:"listen"`
	UserHeader   string   `toml:"user_header"`
	GroupsHeader string   `toml:"groups_header"`
}

type BearerConfig struct {
	Issuer   string            `toml:"issuer"`
	JWKS     string            `toml:"jwks"`
	Userinfo string            `toml:"userinfo"`
	Audience string            `toml:"audience"`
	Types    []string          `toml:"types"`
	Users    []string          `toml:"users"`
	Clients  map[string]string `toml:"clients"`
}

func (l *Loaded) PolicyPath() string {
	p := l.cfg().Authz.Policy
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.dir(), p)
}
