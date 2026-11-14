package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type EndpointConfig struct {
	Web              *WebEndpointConfig `toml:"web"`
	TLS              *TLSEndpointConfig `toml:"tls"`
	EdgeFollowMaxAge string             `toml:"edge_follow_max_age"`
	OutboxBudgetMiB  int                `toml:"outbox_budget_mib"`
}

const DefaultOutboxBudgetMiB = 192

func (l *Loaded) EndpointOutboxBudget() (int, error) {
	mib := l.cfg().Endpoint.OutboxBudgetMiB
	switch {
	case mib == 0:
		return DefaultOutboxBudgetMiB << 20, nil
	case mib < 0:
		return 0, fmt.Errorf("endpoint.outbox_budget_mib %d is not a positive size", mib)
	}
	return mib << 20, nil
}

const defaultEdgeFollowMaxAge = time.Hour

func (l *Loaded) EdgeFollowMaxAge() (time.Duration, error) {
	s := l.cfg().Endpoint.EdgeFollowMaxAge
	if s == "" {
		return defaultEdgeFollowMaxAge, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("endpoint.edge_follow_max_age %q is not a positive duration", s)
	}
	return d, nil
}

type WebEndpointConfig struct {
	Path       string `toml:"path"`
	Mode       string `toml:"mode"`
	MaxConnAge string `toml:"max_conn_age"`
	Authn      string `toml:"authn"`
}

type WebEndpoint struct {
	Path       string
	Mode       os.FileMode
	MaxConnAge time.Duration
	Authn      string
}

const (
	defaultWebEndpointMode       = 0o660
	defaultWebEndpointMaxConnAge = 8 * time.Hour
)

func (l *Loaded) WebEndpoint() (WebEndpoint, bool, error) {
	w := l.cfg().Endpoint.Web
	if w == nil {
		return WebEndpoint{}, false, nil
	}
	if w.Path == "" {
		return WebEndpoint{}, false, fmt.Errorf("endpoint.web: path is required")
	}
	out := WebEndpoint{Path: w.Path, Mode: defaultWebEndpointMode, MaxConnAge: defaultWebEndpointMaxConnAge, Authn: w.Authn}
	if w.Mode != "" {
		m, err := strconv.ParseUint(w.Mode, 8, 32)
		if err != nil || m > 0o777 {
			return WebEndpoint{}, false, fmt.Errorf("endpoint.web: mode %q is not an octal permission", w.Mode)
		}
		out.Mode = os.FileMode(m)
	}
	if w.MaxConnAge != "" {
		d, err := time.ParseDuration(w.MaxConnAge)
		if err != nil || d <= 0 {
			return WebEndpoint{}, false, fmt.Errorf("endpoint.web: max_conn_age %q is not a positive duration", w.MaxConnAge)
		}
		out.MaxConnAge = d
	}
	return out, true, nil
}
