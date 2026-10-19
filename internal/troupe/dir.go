package troupe

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Dir string

func DirFor(stateDir, troupe string) Dir {
	return Dir(filepath.Join(stateDir, "troupe", troupe))
}

func (d Dir) RootPath() string         { return filepath.Join(string(d), "root.crt") }
func (d Dir) IntermediatePath() string { return filepath.Join(string(d), "intermediate.crt") }
func (d Dir) intermediateKey() string  { return filepath.Join(string(d), "intermediate.key") }
func (d Dir) NodePath() string         { return filepath.Join(string(d), "node.crt") }
func (d Dir) nodeKey() string          { return filepath.Join(string(d), "node.key") }
func (d Dir) joinedPath() string       { return filepath.Join(string(d), "joined.json") }

func (d Dir) WriteRoot(root *x509.Certificate) error {
	return d.write(d.RootPath(), certPEM(root))
}

func (d Dir) WriteIntermediate(cert *x509.Certificate, key ed25519.PrivateKey) error {
	k, err := keyPEM(key)
	if err != nil {
		return err
	}
	if err := d.write(d.intermediateKey(), k); err != nil {
		return err
	}
	return d.write(d.IntermediatePath(), certPEM(cert))
}

func (d Dir) WriteNode(leaf, inter *x509.Certificate, key ed25519.PrivateKey) error {
	k, err := keyPEM(key)
	if err != nil {
		return err
	}
	if err := d.write(d.nodeKey(), k); err != nil {
		return err
	}
	return d.write(d.NodePath(), append(certPEM(leaf), certPEM(inter)...))
}

func (d Dir) WriteJoined(es []Entry) error {
	raw, err := json.Marshal(es)
	if err != nil {
		return err
	}
	return d.write(d.joinedPath(), raw)
}

func (d Dir) Joined() ([]Entry, error) {
	raw, err := os.ReadFile(d.joinedPath())
	if err != nil {
		return nil, err
	}
	var es []Entry
	if err := json.Unmarshal(raw, &es); err != nil {
		return nil, fmt.Errorf("%s: %w", d.joinedPath(), err)
	}
	return es, nil
}

func (d Dir) DropJoined() error {
	err := os.Remove(d.joinedPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (d Dir) Root() (*x509.Certificate, error) {
	return readCert(d.RootPath())
}

func (d Dir) Intermediate() (*x509.Certificate, ed25519.PrivateKey, error) {
	return d.IntermediateWithKey(d.intermediateKey())
}

func (d Dir) IntermediateWithKey(keyPath string) (*x509.Certificate, ed25519.PrivateKey, error) {
	cert, err := readCert(d.IntermediatePath())
	if err != nil {
		return nil, nil, err
	}
	key, err := readKey(keyPath)
	if err != nil {
		return nil, nil, err
	}
	if !key.Public().(ed25519.PublicKey).Equal(cert.PublicKey) {
		return nil, nil, fmt.Errorf("%s does not hold the key %s certifies", keyPath, d.IntermediatePath())
	}
	return cert, key, nil
}

func (d Dir) IntermediateKeyAtRest() (string, bool) {
	_, err := os.Stat(d.intermediateKey())
	return d.intermediateKey(), err == nil
}

func (d Dir) WriteNodeCert(leaf, inter *x509.Certificate) error {
	return d.write(d.NodePath(), append(certPEM(leaf), certPEM(inter)...))
}

func (d Dir) Node() (tls.Certificate, error) {
	return d.NodeWithKey(d.nodeKey())
}

func (d Dir) NodeKeyAtRest() (string, bool) {
	_, err := os.Stat(d.nodeKey())
	return d.nodeKey(), err == nil
}

func (d Dir) NodeWithKey(keyPath string) (tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(d.NodePath(), keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return tls.Certificate{}, err
		}
	}
	return pair, nil
}

func (d Dir) write(path string, data []byte) error {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(string(d), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(string(d), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func keyPEM(k ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func readCert(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no certificate", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s holds no key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New(path + ": not an Ed25519 key")
	}
	return ed, nil
}
