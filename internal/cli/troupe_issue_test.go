package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/troupe"
)

const otherNode = "0b7c3a52-5d0e-4d8e-9f53-2a1f6c7e9d41"

func sha(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func tmpfsDir(t *testing.T) string {
	t.Helper()
	for _, base := range []string{os.Getenv("XDG_RUNTIME_DIR"), "/dev/shm", os.TempDir()} {
		if base == "" {
			continue
		}
		if ok, err := isTmpfs(base); err == nil && ok {
			dir, err := os.MkdirTemp(base, "troupe-issue-test-")
			require.NoError(t, err)
			t.Cleanup(func() { os.RemoveAll(dir) })
			return filepath.Join(dir, "out")
		}
	}
	t.Skip("no tmpfs to issue into")
	return ""
}

func diskDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "troupe-issue-disk-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	if ok, err := isTmpfs(dir); err != nil || ok {
		t.Skip("the package directory is on a tmpfs; no disk to refuse")
	}
	return dir
}

// --for issues another node's intermediate into a tmpfs: three 0600 files
// that chain to the root and name that node, and nothing written into this
// node's troupe dir. The output carries fingerprints and nothing secret.
func TestIntermediateForAnotherNodeLandsInTheTmpfsOnly(t *testing.T) {
	state := t.TempDir()
	raw, rootKey := troupeRootPEM(t)
	out := tmpfsDir(t)
	msg, err := troupeIntermediateFor(bytes.NewReader(raw), state, "estate", keyOpts{For: otherNode, Out: out}, time.Now())
	require.NoError(t, err)

	for _, f := range issuedFiles {
		fi, err := os.Stat(filepath.Join(out, f))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), f)
	}
	d := troupe.Dir(out)
	root, err := d.Root()
	require.NoError(t, err)
	want, err := troupe.RootCert("estate", rootKey)
	require.NoError(t, err)
	require.Equal(t, want.Raw, root.Raw)
	inter, ik, err := d.Intermediate()
	require.NoError(t, err)
	require.NoError(t, inter.CheckSignatureFrom(root))
	require.Equal(t, otherNode, troupe.IntermediateNode(inter))

	entries, err := os.ReadDir(state)
	require.NoError(t, err)
	require.Empty(t, entries, "the founder's own troupe dir was touched")

	der, _ := x509.MarshalPKCS8PrivateKey(ik)
	keyFile, _ := os.ReadFile(filepath.Join(out, "intermediate.key"))
	for _, secret := range []string{"PRIVATE KEY", hex.EncodeToString(ik.Seed()), hex.EncodeToString(der), string(keyFile), hex.EncodeToString(rootKey.Seed())} {
		require.NotContains(t, msg, secret)
	}
	require.Contains(t, msg, hex.EncodeToString(sha(root.Raw)))
	require.Contains(t, msg, hex.EncodeToString(sha(inter.Raw)))
	require.Contains(t, msg, hex.EncodeToString(sha(ik.Public().(ed25519.PublicKey))))
	for _, f := range []string{filepath.Join(out, "root.crt"), filepath.Join(out, "intermediate.crt")} {
		b, _ := os.ReadFile(f)
		require.False(t, bytes.Contains(b, rootKey.Seed()) || bytes.Contains(b, raw), "root key material in %s", f)
	}
}

// Disk needs saying so; an existing issue is never overwritten; and a bad
// node id or a key that is not this troupe's root writes nothing.
func TestIntermediateForRefusals(t *testing.T) {
	raw, _ := troupeRootPEM(t)
	issue := func(state string, o keyOpts, root []byte) (string, error) {
		return troupeIntermediateFor(bytes.NewReader(root), state, "estate", o, time.Now())
	}

	disk := diskDir(t)
	_, err := issue(t.TempDir(), keyOpts{For: otherNode, Out: disk}, raw)
	require.ErrorContains(t, err, "not on a tmpfs")
	empty, _ := os.ReadDir(disk)
	require.Empty(t, empty)
	_, err = issue(t.TempDir(), keyOpts{For: otherNode, Out: disk, AllowDisk: true}, raw)
	require.NoError(t, err)

	_, err = issue(t.TempDir(), keyOpts{For: otherNode, Out: disk, AllowDisk: true}, raw)
	require.ErrorContains(t, err, "already holds")

	for _, bad := range []string{"", "node-b", strings.ToUpper(otherNode), "../" + otherNode} {
		out := tmpfsDir(t)
		_, err = issue(t.TempDir(), keyOpts{For: bad, Out: out}, raw)
		require.ErrorContains(t, err, "is not a node id", bad)
		_, statErr := os.Stat(out)
		require.True(t, os.IsNotExist(statErr), "a refused --for %q made %s", bad, out)
	}

	state := t.TempDir()
	_, err = troupeCreate(bytes.NewReader(raw), state, "estate", otherNode, time.Now())
	require.NoError(t, err)
	other, _ := troupeRootPEM(t)
	out := tmpfsDir(t)
	_, err = issue(state, keyOpts{For: otherNode, Out: out}, other)
	require.ErrorContains(t, err, "not troupe estate's root")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "a refused root left %s behind", out)
}
