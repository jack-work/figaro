package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/jack-work/figaro/internal/troupe"
)

type keyOpts struct {
	For       string
	Out       string
	AllowDisk bool
	Root      bool
}

var issuedFiles = []string{"root.crt", "intermediate.crt", "intermediate.key"}

func troupeIntermediateFor(r io.Reader, stateDir, name string, o keyOpts, now time.Time) (msg string, err error) {
	if u, err := uuid.Parse(o.For); err != nil || u.String() != o.For {
		return "", fmt.Errorf("--for %q is not a node id (a lowercase uuid, as figaro troupe ls shows)", o.For)
	}
	out, created, err := issueDir(o.Out, o.AllowDisk)
	if err != nil {
		return "", err
	}
	if created {
		defer func() {
			if err != nil {
				_ = os.Remove(out)
			}
		}()
	}
	key, err := readRoot(r, name)
	if err != nil {
		return "", err
	}
	defer clear(key)
	root, err := troupe.RootCert(name, key)
	if err != nil {
		return "", err
	}
	held, err := troupe.DirFor(stateDir, name).Root()
	switch {
	case err == nil && !bytes.Equal(held.Raw, root.Raw):
		return "", fmt.Errorf("this key is not troupe %s's root (%s)", name, troupe.DirFor(stateDir, name).RootPath())
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	defer clear(ik)
	inter, err := troupe.IssueIntermediate(key, root, name, o.For, ik.Public(), now, troupe.IntermediateTTL)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(ik)
	if err != nil {
		return "", err
	}
	defer clear(der)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	defer clear(keyPEM)
	contents := [][]byte{
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: inter.Raw}),
		keyPEM,
	}
	if err := writeIssued(out, contents); err != nil {
		return "", err
	}
	return fmt.Sprintf("troupe %s: root sha256 %x\nnode %s: intermediate sha256 %x until %s, public key sha256 %x\nwrote %s, %s and %s into %s (0600)\n",
		name, sha256.Sum256(root.Raw), o.For, sha256.Sum256(inter.Raw), inter.NotAfter.Format("2006-01-02"),
		sha256.Sum256(ik.Public().(ed25519.PublicKey)), issuedFiles[0], issuedFiles[1], issuedFiles[2], out), nil
}

func issueDir(dir string, allowDisk bool) (string, bool, error) {
	if dir == "" {
		return "", false, errors.New("--for writes another node's keys into --out <dir>, a tmpfs")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fi = nil
	case err != nil:
		return "", false, err
	case !fi.IsDir():
		return "", false, fmt.Errorf("--out %s is not a directory", dir)
	}
	if !allowDisk {
		at := dir
		if fi == nil {
			at = filepath.Dir(dir)
		}
		onTmpfs, err := isTmpfs(at)
		if err != nil {
			return "", false, err
		}
		if !onTmpfs {
			return "", false, fmt.Errorf("--out %s is not on a tmpfs, and the intermediate key would be at rest there; use a tmpfs (/dev/shm, $XDG_RUNTIME_DIR) or pass --allow-disk", dir)
		}
	}
	for _, f := range issuedFiles {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return "", false, fmt.Errorf("%s already holds %s; issue into an empty directory", dir, f)
		}
	}
	if fi == nil {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", false, err
		}
	}
	return dir, fi == nil, nil
}

func writeIssued(dir string, contents [][]byte) error {
	var written []string
	for i, data := range contents {
		path := filepath.Join(dir, issuedFiles[i])
		if err := writeExclusive(path, data); err != nil {
			for _, w := range written {
				_ = os.Remove(w)
			}
			return err
		}
		written = append(written, path)
	}
	return nil
}

func writeExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
