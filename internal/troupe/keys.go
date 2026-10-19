package troupe

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	IntermediateTTL = 365 * 24 * time.Hour
	NodeTTL         = 7 * 24 * time.Hour
	clockSkew       = 5 * time.Minute
)

var (
	rootNotBefore = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rootNotAfter  = time.Date(2126, 1, 1, 0, 0, 0, 0, time.UTC)
	validName     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

var ErrIntermediateExpired = errors.New("the node intermediate has expired")

func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("troupe name %q: want lowercase letters, digits and inner hyphens, at most 63", name)
	}
	return nil
}

func Identity(troupe, node string) string {
	return (&url.URL{Scheme: "spiffe", Host: troupe, Path: "/node/" + node}).String()
}

func NodeOf(troupe string, u *url.URL) (string, bool) {
	if u.Scheme != "spiffe" || u.Host != troupe {
		return "", false
	}
	node, ok := strings.CutPrefix(u.Path, "/node/")
	return node, ok && node != "" && !strings.Contains(node, "/")
}

func ParseRoot(raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("troupe root: no PEM block on stdin")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("troupe root: %w", err)
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("troupe root: want an Ed25519 key, got %T", k)
	}
	return ed, nil
}

func RootCert(troupe string, key ed25519.PrivateKey) (*x509.Certificate, error) {
	if err := ValidName(troupe); err != nil {
		return nil, err
	}
	pub := key.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(append([]byte(troupe+"\x00"), pub...))
	serial := new(big.Int).SetBytes(sum[:16])
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "figaro troupe " + troupe},
		NotBefore:             rootNotBefore,
		NotAfter:              rootNotAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          sum[16:],
	}
	return create(tmpl, tmpl, pub, key)
}

func IssueIntermediate(root ed25519.PrivateKey, rootCert *x509.Certificate, troupe, node string, pub crypto.PublicKey, now time.Time, ttl time.Duration) (*x509.Certificate, error) {
	if err := ValidName(troupe); err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "figaro troupe " + troupe + " node " + node, SerialNumber: node},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              earlier(now.Add(ttl), rootCert.NotAfter),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedURIDomains:   []string{troupe},
	}
	return create(tmpl, rootCert, pub, root)
}

func IntermediateNode(c *x509.Certificate) string {
	return c.Subject.SerialNumber
}

func IssueNode(inter crypto.Signer, interCert *x509.Certificate, troupe, node string, pub crypto.PublicKey, now time.Time, ttl time.Duration) (*x509.Certificate, error) {
	if !now.Before(interCert.NotAfter) {
		return nil, fmt.Errorf("%w at %s", ErrIntermediateExpired, interCert.NotAfter.Format(time.RFC3339))
	}
	id, err := url.Parse(Identity(troupe, node))
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: node},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     earlier(now.Add(ttl), interCert.NotAfter),
		URIs:         []*url.URL{id},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	return create(tmpl, interCert, pub, inter)
}

func create(tmpl, parent *x509.Certificate, pub crypto.PublicKey, signer crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func ParseSigned(chain [][]byte, rootDER []byte) (leaf, issuer, root *x509.Certificate, err error) {
	if len(chain) != 2 {
		return nil, nil, nil, fmt.Errorf("want a leaf and its issuer, got %d certificates", len(chain))
	}
	if leaf, err = x509.ParseCertificate(chain[0]); err != nil {
		return nil, nil, nil, err
	}
	if issuer, err = x509.ParseCertificate(chain[1]); err != nil {
		return nil, nil, nil, err
	}
	if root, err = x509.ParseCertificate(rootDER); err != nil {
		return nil, nil, nil, err
	}
	return leaf, issuer, root, nil
}

const rejoinDomain = "figaro troupe rejoin v1\n"

func rejoinMessage(troupe, node string, pub ed25519.PublicKey, door string) []byte {
	msg := []byte(rejoinDomain + troupe + "\n" + node + "\n" + door + "\n")
	return append(msg, pub...)
}

func SignRejoin(old crypto.Signer, troupe, node string, pub ed25519.PublicKey, door string) ([]byte, error) {
	return old.Sign(rand.Reader, rejoinMessage(troupe, node, pub, door), crypto.Hash(0))
}

func CheckRejoin(leaf, issuer, root *x509.Certificate, sig []byte, troupe, node string, pub ed25519.PublicKey, door string) error {
	if !names(leaf, Identity(troupe, node)) {
		return fmt.Errorf("the old certificate does not name %s", Identity(troupe, node))
	}
	during := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) / 2)
	if err := chainsTo(leaf, issuer, root, during); err != nil {
		return fmt.Errorf("the old certificate: %w", err)
	}
	old, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(old, rejoinMessage(troupe, node, pub, door), sig) {
		return errors.New("the request is not signed by the old certificate's key")
	}
	return nil
}
