package peertest

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

type PKI struct {
	t    testing.TB
	dir  string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	CA   string
}

func NewPKI(t testing.TB, name string) *PKI {
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
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	p := &PKI{t: t, dir: t.TempDir(), cert: cert, key: key}
	p.CA = p.write(name+"-ca.crt", "CERTIFICATE", der)
	return p
}

func Identity(node string) string { return "spiffe://test.figaro/node/" + node }

func (p *PKI) write(name, typ string, der []byte) string {
	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		p.t.Fatal(err)
	}
	return path
}

func (p *PKI) Node(name, uri string) (cert, key string) {
	p.t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		p.t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		p.t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		p.t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{u},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &k.PublicKey, p.key)
	if err != nil {
		p.t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		p.t.Fatal(err)
	}
	return p.write(name+".crt", "CERTIFICATE", der), p.write(name+".key", "EC PRIVATE KEY", kder)
}

func (p *PKI) Door(node, listen string) config.TLSDoor {
	cert, key := p.Node(node+"-door", Identity(node))
	return config.TLSDoor{Listen: listen, Cert: cert, Key: key, ClientCA: p.CA}
}

func (p *PKI) Peer(as, to, addr string) config.Peer {
	cert, key := p.Node(as+"-to-"+to, Identity(as))
	return config.Peer{Node: to, Address: addr, Identity: Identity(to), CA: p.CA, Cert: cert, Key: key}
}
