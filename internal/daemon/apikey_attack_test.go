package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/troupe"
)

func estateIntermediate(t *testing.T, root estateRoot, node string) (ed25519.PrivateKey, *x509.Certificate) {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c, err := troupe.IssueIntermediate(root.key, root.cert, "estate", node, k.Public(), time.Now(), troupe.IntermediateTTL)
	require.NoError(t, err)
	return k, c
}

// The attack the setters rule closes: troupe policy grants a keyed
// principal by name, so an intermediate the policy does not name a setter
// mints a key for that principal, good on every node. The ledger takes the
// entry, and no node admits the key; a setter's key for the same principal
// is admitted everywhere it names.
func TestAnUnnamedIntermediateCannotMintForAGrantedPrincipal(t *testing.T) {
	root := newEstateRoot(t)
	const granted = "service:ci@troupe:estate"
	policy, err := troupe.SignRootPolicy(root.key, "estate", 1, "root:estate",
		"setters = [\"node-setter\"]\n[roles.ci]\ngrants = [{ verbs = [\"*\"], on = [\"*\"] }]\n[[bind]]\nrole = \"ci\"\nprincipals = [\""+granted+"\"]\n", time.Now())
	require.NoError(t, err)

	mint := func(node string) (string, troupe.Entry) {
		k, c := estateIntermediate(t, root, node)
		wire, key, err := apikey.New(rand.Reader)
		require.NoError(t, err)
		e, err := troupe.SignAPIKey(k, c, "estate", key.ID, granted, key.Hash[:], time.Now().Add(time.Hour), []string{apikey.AllNodes}, "user:mallory", time.Now())
		require.NoError(t, err)
		return wire, e
	}
	rogue, rogueEntry := mint("node-rogue")
	honest, honestEntry := mint("node-setter")

	l := troupe.NewLedger("estate", root.cert)
	_, err = l.Merge(policy, rogueEntry, honestEntry)
	require.NoError(t, err, "the entries verify; what decides is who signed them")
	grants := apiKeyGrants(l.View().APIKeys)

	for _, node := range []string{"node-setter", "node-rogue", "node-other"} {
		keys, err := authz.NewAPIKeys("estate", node, nil, nil)
		require.NoError(t, err)
		require.NoError(t, keys.SetTroupe(grants))
		_, err = keys.Admit(context.Background(), rogue)
		require.ErrorIs(t, err, authz.ErrKeyRevoked, node)
		require.ErrorContains(t, err, "not a setter", node)
		_, err = keys.Admit(context.Background(), honest)
		require.NoError(t, err, node)
	}
}
