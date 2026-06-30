// Package oauth provides provider-agnostic OAuth 2.0 mechanics for
// figaro: PKCE generation, the standard refresh_token grant, and the
// credential payload that lands in the credential store.
//
// This package is deliberately ignorant of hush: it speaks HTTP and
// returns plain values. Storage lives behind the credstore interface.
package oauth

import (
	"errors"
	"time"
)

// Credential is the figaro-owned OAuth credential payload. Stored
// opaquely by the vault; only this package and the per-provider
// implementations interpret the fields.
//
// `Extra` carries provider-specific metadata that needs to ride along
// with the credential — e.g. GitHub Copilot's enterprise domain,
// available model ids, or proxy endpoint hints derived from the token.
// Keys are provider-namespaced strings ("copilot.enterprise_url").
type Credential struct {
	Access    string            `json:"access"`
	Refresh   string            `json:"refresh"`
	ExpiresAt time.Time         `json:"expires_at"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// Expired reports whether the credential is past its expiry. A zero
// ExpiresAt is treated as never-expires (callers seeding from
// non-time-bound flows can leave it zero).
func (c Credential) Expired(now time.Time) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(c.ExpiresAt)
}

// NeedsRefresh reports whether the credential is within `window` of its
// expiry — used by resolvers to refresh proactively.
func (c Credential) NeedsRefresh(now time.Time, window time.Duration) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return !now.Add(window).Before(c.ExpiresAt)
}

// ErrRefreshTransient signals a recoverable failure (network blip, 5xx).
// Callers should retry with the same refresh token.
var ErrRefreshTransient = errors.New("oauth: refresh failed transiently")

// ErrRefreshPermanent signals the refresh token itself was rejected
// (4xx). Callers should surface a re-login prompt; retrying with the
// same refresh token won't help.
var ErrRefreshPermanent = errors.New("oauth: refresh failed permanently (re-login required)")

// DefaultSafetyWindow is subtracted from a provider's expires_in when
// computing absolute expiry, so the credential is treated as stale
// before the server actually rejects it. Matches the hush behavior we
// inherit from.
const DefaultSafetyWindow = 5 * time.Minute

// DefaultProactiveWindow is how far ahead of expiry a resolver should
// refresh on its own without waiting for a 401.
const DefaultProactiveWindow = 10 * time.Minute
