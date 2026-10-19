package angelus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkauthz"
)

const documentWithAnIssuer = `
[roles.r]
grants = [{ verbs = ["read"], on = ["*"] }]

[[bind]]
role = "r"
principals = ["user:ana@id.example.org"]

[[issuers]]
issuer = "https://id.example.org"
jwks = "https://id.example.org/jwks"
clients = { box = "sandbox" }
`

// A node that opts out of troupe issuers takes the troupe's grants but
// fetches keys from no issuer the troupe names.
func TestATroupeDocumentsIssuersAreTakenUnlessTheNodeOptsOut(t *testing.T) {
	for _, take := range []bool{true, false} {
		is, err := authz.NewIssuers(nil, nil, nil)
		require.NoError(t, err)
		a := &Angelus{Policies: authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil), Issuers: is}
		require.NoError(t, a.Policies.Reload(""))
		require.NoError(t, a.ApplyTroupeDocument("estate", []byte(documentWithAnIssuer), take))
		require.Equal(t, take, !is.Empty(), "take=%v", take)
		require.Equal(t, "estate", a.Policies.TroupeName())

		require.NoError(t, a.ApplyTroupeDocument("estate", nil, take))
		require.True(t, is.Empty(), "the issuer outlived its document")
	}
}
