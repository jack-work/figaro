package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/troupe"
)

func troupeRootPEM(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), k
}

// troupe create writes the root certificate and this node's intermediate,
// and the root key itself lands nowhere on disk.
func TestTroupeCreateWritesTheRootCertAndAnIntermediateButNeverTheRootKey(t *testing.T) {
	state := t.TempDir()
	raw, key := troupeRootPEM(t)
	now := time.Now()
	out, err := troupeCreate(bytes.NewReader(raw), state, "estate", "node-a", now)
	require.NoError(t, err)
	require.Contains(t, out, "node-a")

	d := troupe.DirFor(state, "estate")
	root, err := d.Root()
	require.NoError(t, err)
	want, err := troupe.RootCert("estate", key)
	require.NoError(t, err)
	require.Equal(t, want.Raw, root.Raw)
	inter, _, err := d.Intermediate()
	require.NoError(t, err)
	require.NoError(t, inter.CheckSignatureFrom(root))
	require.WithinDuration(t, now.Add(troupe.IntermediateTTL), inter.NotAfter, time.Minute)

	seed := key.Seed()
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	_ = filepath.Walk(state, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		require.False(t, bytes.Contains(b, seed) || bytes.Contains(b, der) || bytes.Contains(b, raw), "root key material in %s", p)
		return nil
	})
}

// troupe intermediate renews this node's intermediate with the same root,
// and refuses a key that is not this troupe's root.
func TestTroupeIntermediateRenewsWithTheRootAndRefusesAnother(t *testing.T) {
	state := t.TempDir()
	raw, _ := troupeRootPEM(t)
	_, err := troupeCreate(bytes.NewReader(raw), state, "estate", "node-a", time.Now())
	require.NoError(t, err)
	d := troupe.DirFor(state, "estate")
	first, _, err := d.Intermediate()
	require.NoError(t, err)

	later := time.Now().Add(300 * 24 * time.Hour)
	_, err = troupeIntermediate(bytes.NewReader(raw), state, "estate", "node-a", later)
	require.NoError(t, err)
	second, _, err := d.Intermediate()
	require.NoError(t, err)
	require.NotEqual(t, first.SerialNumber, second.SerialNumber)
	require.True(t, second.NotAfter.After(first.NotAfter))

	other, _ := troupeRootPEM(t)
	_, err = troupeIntermediate(bytes.NewReader(other), state, "estate", "node-a", later)
	require.ErrorContains(t, err, "not troupe estate's root")
	_, err = troupeCreate(bytes.NewReader(other), state, "estate", "node-a", later)
	require.ErrorContains(t, err, "already")
}

// A second node joins the same static troupe with the same root: troupe
// intermediate on a node with no root.crt yet derives and writes it.
func TestASecondNodeGetsItsIntermediateFromTheSameRoot(t *testing.T) {
	raw, _ := troupeRootPEM(t)
	a, b := t.TempDir(), t.TempDir()
	_, err := troupeCreate(bytes.NewReader(raw), a, "estate", "node-a", time.Now())
	require.NoError(t, err)
	_, err = troupeIntermediate(bytes.NewReader(raw), b, "estate", "node-b", time.Now())
	require.NoError(t, err)
	ra, _ := troupe.DirFor(a, "estate").Root()
	rb, _ := troupe.DirFor(b, "estate").Root()
	require.Equal(t, ra.Raw, rb.Raw)
}

func TestTroupeKeyVerbsRefuseWhatIsNotARoot(t *testing.T) {
	_, err := troupeCreate(strings.NewReader("hello"), t.TempDir(), "estate", "node-a", time.Now())
	require.Error(t, err)
	raw, _ := troupeRootPEM(t)
	_, err = troupeCreate(bytes.NewReader(raw), t.TempDir(), "Bad Name", "node-a", time.Now())
	require.Error(t, err)
}
