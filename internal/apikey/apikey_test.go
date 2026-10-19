package apikey

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"
	"github.com/stretchr/testify/require"
)

func TestAMintedKeyParsesToItsOwnIDAndHash(t *testing.T) {
	wire, k, err := New(rand.Reader)
	require.NoError(t, err)
	require.True(t, Is(wire))
	require.Len(t, wire, wireLen)
	got, err := Parse(wire)
	require.NoError(t, err)
	require.Equal(t, k, got)
	require.True(t, got.Matches(k.Hash[:]))
	require.NoError(t, ValidID(k.ID))
}

// The checksum catches a typo before any lookup, and the hash covers the id,
// so a secret moved under another id does not match.
func TestAnAlteredKeyIsMalformedOrMatchesNothing(t *testing.T) {
	wire, k, err := New(rand.Reader)
	require.NoError(t, err)
	for i := len(Prefix); i < len(wire); i++ {
		if wire[i] == '_' {
			continue
		}
		b := []byte(wire)
		b[i] = map[bool]byte{true: 'b', false: 'a'}[b[i] == 'a']
		got, err := Parse(string(b))
		if err == nil && got.Matches(k.Hash[:]) {
			t.Fatalf("flipping byte %d still matches", i)
		}
	}
	_, other, err := New(rand.Reader)
	require.NoError(t, err)
	moved := Prefix + other.ID + wire[len(Prefix)+idLen:]
	if got, err := Parse(moved); err == nil && got.Matches(k.Hash[:]) {
		t.Fatal("a secret under another id matched")
	}
}

func TestAJWTOrJunkIsNotAKey(t *testing.T) {
	for _, s := range []string{"", "fgk_", "eyJhbGciOiJFZERTQSJ9.e30.sig", strings.Repeat("a", wireLen), "fgk_" + strings.Repeat("A", wireLen-4)} {
		_, err := Parse(s)
		require.ErrorIs(t, err, ErrMalformed, s)
	}
	require.False(t, Is("eyJhbGciOiJFZERTQSJ9.e30.sig"))
}

func TestMatchesRefusesAWrongLengthHash(t *testing.T) {
	_, k, err := New(rand.Reader)
	require.NoError(t, err)
	require.False(t, k.Matches(k.Hash[:31]))
	require.False(t, k.Matches(nil))
}

func TestNewReadsTheWholeRandomBuffer(t *testing.T) {
	_, _, err := New(bytes.NewReader(make([]byte, 10)))
	require.Error(t, err)
}

// A key principal is scoped by its troupe, and cannot pass for an issuer's
// principal, a node, a sandbox or the troupe itself.
func TestAKeyPrincipalIsAUserOrServiceScopedByItsTroupe(t *testing.T) {
	p, err := Principal("service", "ci", "estate")
	require.NoError(t, err)
	require.Equal(t, "service:ci@troupe:estate", p.String())
	ok := []string{"service:ci@troupe:estate", "user:alice@troupe:estate"}
	for _, s := range ok {
		_, err := ParsePrincipal(s, "estate")
		require.NoError(t, err, s)
	}
	bad := []string{
		"service:ci@troupe:other",
		"service:ci@auth.kelliher.info",
		"user:jack@auth.kelliher.info",
		"service:ci",
		"sandbox:ci@troupe:estate",
		"node:ci@troupe:estate",
		"troupe:estate",
		"spain/service:ci@troupe:estate",
		"service:@troupe:estate",
		"service:a@b@troupe:estate",
		"service:" + strings.Repeat("x", MaxName+1) + "@troupe:estate",
	}
	for _, s := range bad {
		_, err := ParsePrincipal(s, "estate")
		require.Error(t, err, s)
	}
	require.Error(t, CheckPrincipal(jkauthz.Principal{Kind: "service", ID: "ci@troupe:estate", Node: "n"}, "estate"))
}
