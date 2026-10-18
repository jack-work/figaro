package peer_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// pki mints a throwaway federation CA and node certificates the way the
// flake is expected to: one cert per node, a SPIFFE URI SAN, both EKUs.
type pki struct {
	t    *testing.T
	dir  string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	ca   string
}

func newPKI(t *testing.T, name string) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	p := &pki{t: t, dir: t.TempDir(), cert: cert, key: key}
	p.ca = p.write(name+"-ca.crt", "CERTIFICATE", der)
	return p
}

func (p *pki) write(name, typ string, der []byte) string {
	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		p.t.Fatal(err)
	}
	return path
}

func identity(node string) string { return "spiffe://test.figaro/node/" + node }

// node returns cert and key paths for a node certificate carrying uri.
func (p *pki) node(name, uri string) (string, string) {
	p.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		p.t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		p.t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{u},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		p.t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		p.t.Fatal(err)
	}
	return p.write(name+".crt", "CERTIFICATE", der), p.write(name+".key", "EC PRIVATE KEY", kder)
}

func (p *pki) door(node, listen string) config.TLSDoor {
	cert, key := p.node(node+"-door", identity(node))
	return config.TLSDoor{Listen: listen, Cert: cert, Key: key, ClientCA: p.ca}
}

// peer is how the dialling node configures node `to`, presenting its own identity `as`.
func (p *pki) peer(as, to, addr string) config.Peer {
	cert, key := p.node(as+"-to-"+to, identity(as))
	return config.Peer{Node: to, Address: addr, Identity: identity(to), CA: p.ca, Cert: cert, Key: key}
}
