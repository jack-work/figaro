package authz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/config"
	"github.com/stretchr/testify/require"
)

type keyClock struct{ at atomic.Int64 }

func (c *keyClock) now() time.Time          { return time.Unix(0, c.at.Load()) }
func (c *keyClock) advance(d time.Duration) { c.at.Add(int64(d)) }
func newKeyClock() *keyClock {
	c := &keyClock{}
	c.at.Store(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func mintKey(t testing.TB) (string, apikey.Key) {
	t.Helper()
	wire, k, err := apikey.New(rand.Reader)
	require.NoError(t, err)
	return wire, k
}

func grant(k apikey.Key, principal string, notAfter time.Time) APIKeyGrant {
	return APIKeyGrant{ID: k.ID, Principal: principal, Hash: k.Hash[:], NotAfter: notAfter}
}

func TestAKeyAdmitsItsPrincipalWithNoGroups(t *testing.T) {
	c := newKeyClock()
	keys, err := NewAPIKeys("estate", nil, c.now)
	require.NoError(t, err)
	wire, k := mintKey(t)
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{grant(k, "service:ci@troupe:estate", c.now().Add(time.Hour))}))
	ctx, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	s, err := SubjectOf(ctx)
	require.NoError(t, err)
	require.Equal(t, "service:ci@troupe:estate", s.Principal.String())
	require.Empty(t, s.Groups)
	tok, ok := BearerOf(ctx)
	require.True(t, ok)
	require.Equal(t, wire, tok)
}

// Unknown id, wrong secret and junk read the same to the caller, so a
// presentation tells an attacker nothing about which ids exist beyond the
// id it already sent.
func TestAWrongKeyIsRefusedTheSameWayWhateverIsWrong(t *testing.T) {
	c := newKeyClock()
	keys, err := NewAPIKeys("estate", nil, c.now)
	require.NoError(t, err)
	_, k := mintKey(t)
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{grant(k, "service:ci@troupe:estate", c.now().Add(time.Hour))}))
	other, _ := mintKey(t)
	sameID := apikey.Prefix + k.ID + other[len(apikey.Prefix)+10:]
	for name, tok := range map[string]string{"unknown id": other, "junk": "fgk_nope", "jwt": "eyJhbGciOiJSUzI1NiJ9.e30.x", "secret under the id": sameID} {
		_, err := keys.Admit(context.Background(), tok)
		require.ErrorIs(t, err, ErrUnadmitted, name)
		require.False(t, errors.Is(err, ErrKeyRevoked), name)
	}
}

// A revoke reaching the node closes every connection that key holds, and
// the next call on one that is still draining is refused.
func TestARevokeCutsTheKeysLiveConnections(t *testing.T) {
	c := newKeyClock()
	keys, err := NewAPIKeys("estate", nil, c.now)
	require.NoError(t, err)
	wire, k := mintKey(t)
	other, ko := mintKey(t)
	g, go2 := grant(k, "service:ci@troupe:estate", c.now().Add(time.Hour)), grant(ko, "service:cd@troupe:estate", c.now().Add(time.Hour))
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{g, go2}))
	a, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	b, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	kept, err := keys.Admit(context.Background(), other)
	require.NoError(t, err)
	require.Equal(t, 3, keys.liveConns())

	g.Void = "revoked by user:owner"
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{g, go2}))
	for _, ctx := range []context.Context{a, b} {
		<-ctx.Done()
		require.ErrorIs(t, context.Cause(ctx), ErrKeyRevoked)
		_, err := SubjectOf(ctx)
		require.ErrorIs(t, err, ErrKeyRevoked)
	}
	require.NoError(t, kept.Err())
	_, err = keys.Admit(context.Background(), wire)
	require.ErrorIs(t, err, ErrKeyRevoked)
	require.Eventually(t, func() bool { return keys.liveConns() == 1 }, time.Second, time.Millisecond)

	require.NoError(t, keys.SetTroupe(nil))
	<-kept.Done()
	require.ErrorIs(t, context.Cause(kept), ErrKeyRevoked)
}

// Every call re-checks the key, so an expired key stops working on an open
// connection, and a fresh upgrade with it is refused.
func TestAnExpiredKeyIsRefusedOnTheNextCall(t *testing.T) {
	c := newKeyClock()
	keys, err := NewAPIKeys("estate", nil, c.now)
	require.NoError(t, err)
	wire, k := mintKey(t)
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{grant(k, "service:ci@troupe:estate", c.now().Add(time.Hour))}))
	ctx, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	c.advance(time.Hour)
	_, err = SubjectOf(ctx)
	require.ErrorIs(t, err, ErrExpired)
	_, err = keys.Admit(context.Background(), wire)
	require.ErrorIs(t, err, ErrExpired)
}

func TestAnAdmittedConnectionClosesWhenItsKeyExpires(t *testing.T) {
	keys, err := NewAPIKeys("estate", nil, nil)
	require.NoError(t, err)
	wire, k := mintKey(t)
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{grant(k, "service:ci@troupe:estate", time.Now().Add(50*time.Millisecond))}))
	ctx, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	select {
	case <-ctx.Done():
		require.ErrorIs(t, context.Cause(ctx), ErrExpired)
	case <-time.After(5 * time.Second):
		t.Fatal("still open past the key's expiry")
	}
}

// A key from another troupe, or one naming an issuer's principal, is not
// taken up even if a ledger carried it.
func TestOnlyThisTroupesKeyPrincipalsAreTakenUp(t *testing.T) {
	c := newKeyClock()
	keys, err := NewAPIKeys("estate", nil, c.now)
	require.NoError(t, err)
	wire, k := mintKey(t)
	err = keys.SetTroupe([]APIKeyGrant{grant(k, "user:jack@auth.kelliher.info", c.now().Add(time.Hour))})
	require.Error(t, err)
	_, err = keys.Admit(context.Background(), wire)
	require.ErrorIs(t, err, ErrUnadmitted)
}

// Static keys come from local config as a hash; they outrank a troupe entry
// under the same id.
func TestStaticKeysAreHashesInConfigAndOutrankTheTroupe(t *testing.T) {
	c := newKeyClock()
	wire, k := mintKey(t)
	cfg := []config.APIKeyConfig{{ID: k.ID, Principal: "service:ci@troupe:estate", SHA256: hex.EncodeToString(k.Hash[:]), NotAfter: c.now().Add(time.Hour)}}
	keys, err := NewAPIKeys("estate", cfg, c.now)
	require.NoError(t, err)
	_, other := mintKey(t)
	require.NoError(t, keys.SetTroupe([]APIKeyGrant{{ID: k.ID, Principal: "service:cd@troupe:estate", Hash: other.Hash[:], NotAfter: c.now().Add(time.Hour)}}))
	ctx, err := keys.Admit(context.Background(), wire)
	require.NoError(t, err)
	s, err := SubjectOf(ctx)
	require.NoError(t, err)
	require.Equal(t, "service:ci@troupe:estate", s.Principal.String())

	_, err = NewAPIKeys("", cfg, c.now)
	require.Error(t, err, "no troupe to scope by")
	bad := cfg[0]
	bad.NotAfter = time.Time{}
	_, err = NewAPIKeys("estate", []config.APIKeyConfig{bad}, c.now)
	require.Error(t, err, "a key that never expires")
	bad = cfg[0]
	bad.SHA256 = "abcd"
	_, err = NewAPIKeys("estate", []config.APIKeyConfig{bad}, c.now)
	require.Error(t, err, "a short hash")
}

// The dispatcher sends a key only to the key table and a JWT only to the
// issuers: a key never reaches an issuer's JWKS or userinfo endpoint.
func TestAKeyNeverReachesAnIssuer(t *testing.T) {
	var hits atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer trap.Close()
	is, err := NewIssuers([]config.IssuerConfig{{Issuer: "https://auth.example", JWKS: trap.URL, Userinfo: trap.URL, Users: []string{"figaro-cli"}}}, nil, nil)
	require.NoError(t, err)
	c := newKeyClock()
	keys, err := NewAPIKeys("estate", nil, c.now)
	require.NoError(t, err)
	wire, _ := mintKey(t)

	_, err = (Bearers{Issuers: is, Keys: keys}).Verify(context.Background(), wire)
	require.ErrorIs(t, err, ErrUnadmitted)
	_, err = (Bearers{Issuers: is}).Verify(context.Background(), wire)
	require.ErrorIs(t, err, ErrUnadmitted)
	_, err = is.Open(context.Background(), wire)
	require.ErrorIs(t, err, ErrUnadmitted)
	require.Zero(t, hits.Load())
}

func TestAJWTNeverReachesTheKeyTable(t *testing.T) {
	is := newTestIssuer(t)
	b := Bearers{Issuers: is.bearer(t), Keys: nil}
	tok := is.mint("u-1", "figaro-cli", time.Minute, map[string]any{"sub": "u-1", "preferred_username": "gluck"})
	ctx, err := b.Verify(context.Background(), tok)
	require.NoError(t, err)
	s, err := SubjectOf(ctx)
	require.NoError(t, err)
	require.Equal(t, "gluck@auth.kelliher.info", s.Principal.ID)

	_, err = (Bearers{}).Verify(context.Background(), tok)
	require.ErrorIs(t, err, ErrUnadmitted)
}
