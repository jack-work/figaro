package angelus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer/peertest"
)

// Two issuers that name the same principal never share an @arias watch: the
// watch is keyed by the issuer as well as the principal and its groups. An
// issuer's scope drops the scheme, so http and https at one host collide.
func TestTwoIssuersNamingOnePrincipalGetTwoWatches(t *testing.T) {
	open := func(url string) context.Context {
		is := peertest.NewIssuerAt(t, url)
		issuers, err := authz.NewIssuers(is.Config("probe"), nil, nil)
		require.NoError(t, err)
		s, err := issuers.Open(context.Background(), is.Mint("probe", time.Hour))
		require.NoError(t, err)
		return authz.WithSession(context.Background(), s)
	}
	a, b := open("https://issuer.test.figaro"), open("http://issuer.test.figaro")
	sa, _ := authz.SubjectOf(a)
	sb, _ := authz.SubjectOf(b)
	require.Equal(t, sa.Principal, sb.Principal, "the fixture needs two issuers that name one principal")
	require.NotEqual(t, authz.IssuerOf(a), authz.IssuerOf(b))
	require.NotEqual(t, ariasKey(a), ariasKey(b))
}
