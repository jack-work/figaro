package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// A SESSION TOKEN is a short-lived credential a provider derives from a
// durable one: GitHub access token -> Copilot session token, and anything
// else that trades a long-lived secret for a minutes-long bearer.
//
// It is a property of the CREDENTIAL, not of the conversation. figaro used
// to cache it on the provider instance, and provider instances are per-aria
// (internal/figaro/provbind.go builds one per agent, and rebuilds on every
// provider or knob change). So a fleet of N arias meant N exchanges against
// the same endpoint, N caches expiring on N clocks, and a thundering herd
// every time they aged out together. GitHub answers that burst with 403 and
// its HTML error page, which figaro then reported as "no provider connected"
// - a login prompt for a credential that was never missing.
//
// SessionCache moves the derived token to where its identity actually lives:
// one process-wide cache keyed by credential, with single-flight refresh, so
// K concurrent misses on one key make exactly one round-trip and every aria
// on that credential shares the result.

// Session is a short-lived derived credential.
type Session struct {
	// Token is the bearer to present. Never logged, never rendered.
	Token string
	// ExpiresAt is when the token stops being usable. Exchangers are
	// expected to subtract their own safety margin before returning.
	ExpiresAt time.Time
	// Endpoint is the API host the exchange pointed at, for providers
	// (Copilot) that discover routing along with the credential. "" when
	// the exchange says nothing about routing.
	Endpoint string
}

// Valid reports whether s can still be presented at t.
func (s Session) Valid(t time.Time) bool {
	return s.Token != "" && t.Before(s.ExpiresAt)
}

// ExchangeFunc mints a fresh Session from whatever durable credential the
// caller holds. It runs under the key's single-flight lock: exactly one
// call per key is in progress at a time, and the durable credential is
// resolved inside it, so a cache hit costs no keystore round-trip.
type ExchangeFunc func() (Session, error)

// SessionCache holds one derived credential per key.
//
// The zero value is not usable; take Sessions or call NewSessionCache.
type SessionCache struct {
	mu      sync.Mutex
	entries map[string]*sessionEntry
	now     func() time.Time
}

type sessionEntry struct {
	// flight serializes exchanges for this key. Held across the HTTP
	// call, so a slow exchange blocks only its own key.
	flight sync.Mutex

	mu        sync.Mutex
	sess      Session
	exchanges int
	bindings  int
	last      time.Time
}

// Sessions is the process-wide cache. One angelus, one set of credentials,
// one session token each.
var Sessions = NewSessionCache()

func NewSessionCache() *SessionCache {
	return &SessionCache{entries: map[string]*sessionEntry{}, now: time.Now}
}

func (c *SessionCache) entry(key string) *sessionEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		e = &sessionEntry{}
		c.entries[key] = e
	}
	return e
}

// Bind records that another token source is sharing this key. Purely
// observational: it is what lets `figaro doctor provider` say "one session,
// four arias" instead of leaving sharing a matter of faith.
func (c *SessionCache) Bind(key string) {
	e := c.entry(key)
	e.mu.Lock()
	e.bindings++
	e.mu.Unlock()
}

// Resolve returns the live session for key, exchanging only when the cached
// one is missing or expired. Concurrent callers on one key collapse into a
// single exchange; the losers re-check the cache and take the winner's
// result rather than repeating the call.
func (c *SessionCache) Resolve(key string, exchange ExchangeFunc) (Session, error) {
	if s, ok := c.Peek(key); ok {
		return s, nil
	}
	e := c.entry(key)

	e.flight.Lock()
	defer e.flight.Unlock()

	// Someone may have refreshed this key while we waited for the flight
	// lock. Their token is as good as ours would have been.
	if s, ok := c.peek(e); ok {
		return s, nil
	}

	sess, err := exchange()
	if err != nil {
		return Session{}, err
	}

	e.mu.Lock()
	e.sess = sess
	e.exchanges++
	e.last = c.now()
	e.mu.Unlock()

	return sess, nil
}

// Peek returns the cached session for key without exchanging.
func (c *SessionCache) Peek(key string) (Session, bool) {
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if !ok {
		return Session{}, false
	}
	return c.peek(e)
}

func (c *SessionCache) peek(e *sessionEntry) (Session, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.sess.Valid(c.now()) {
		return Session{}, false
	}
	return e.sess, true
}

// Invalidate drops the cached session for key when it is the one that was
// rejected. The token argument guards against a straggler 401 from a
// request that departed before a refresh: without it, one late failure
// would throw away the good token everyone else is already using.
//
// A shared cache means invalidation is shared too, and that is correct: a
// revoked credential is revoked for every aria, and the next Resolve heals
// all of them with one exchange.
func (c *SessionCache) Invalidate(key, token string) {
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if token == "" || e.sess.Token == token {
		e.sess = Session{}
	}
}

// SessionInfo is one cache entry, described without its secret.
type SessionInfo struct {
	Key string `json:"key"`
	// Fingerprint is a truncated SHA-256 of the token: enough to prove two
	// arias hold the SAME session, useless for presenting it anywhere.
	Fingerprint  string    `json:"fingerprint,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Exchanges    int       `json:"exchanges"`
	Bindings     int       `json:"bindings"`
	LastExchange time.Time `json:"last_exchange,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
}

// Snapshot describes every cached session, secrets replaced by fingerprints.
func (c *SessionCache) Snapshot() []SessionInfo {
	c.mu.Lock()
	keys := make([]string, 0, len(c.entries))
	entries := make([]*sessionEntry, 0, len(c.entries))
	for k, e := range c.entries {
		keys = append(keys, k)
		entries = append(entries, e)
	}
	c.mu.Unlock()

	out := make([]SessionInfo, 0, len(keys))
	for i, k := range keys {
		e := entries[i]
		e.mu.Lock()
		info := SessionInfo{
			Key:          k,
			Fingerprint:  Fingerprint(e.sess.Token),
			Exchanges:    e.exchanges,
			Bindings:     e.bindings,
			LastExchange: e.last,
			Endpoint:     e.sess.Endpoint,
		}
		if e.sess.Token != "" {
			info.ExpiresAt = e.sess.ExpiresAt
		}
		e.mu.Unlock()
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Fingerprint is the only form a token may leave this package in.
func Fingerprint(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:12]
}
