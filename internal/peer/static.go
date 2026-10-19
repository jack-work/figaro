package peer

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/jack-work/figaro/internal/config"
)

type Member struct {
	Node     string
	Address  string
	Identity string
	tls      *tls.Config
}

type Static struct {
	version    uint64
	nodes      []string
	byNode     map[string]*Member
	byIdentity map[string]string
}

type Credentials struct {
	Certificate func() (*tls.Certificate, error)
	Roots       func() (*x509.CertPool, error)
}

func NewStatic(version uint64, peers []config.Peer, creds *Credentials) (*Static, error) {
	m := &Static{version: version, byNode: map[string]*Member{}, byIdentity: map[string]string{}}
	for _, p := range peers {
		cfg, err := memberTLS(p, creds)
		if err != nil {
			return nil, fmt.Errorf("peer %s: %w", p.Node, err)
		}
		m.nodes = append(m.nodes, p.Node)
		m.byNode[p.Node] = &Member{Node: p.Node, Address: p.Address, Identity: p.Identity, tls: cfg}
		m.byIdentity[p.Identity] = p.Node
	}
	return m, nil
}

func memberTLS(p config.Peer, creds *Credentials) (*tls.Config, error) {
	if p.Cert == "" {
		if creds == nil {
			return nil, errors.New("no certificate files and no troupe keys")
		}
		return &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				return creds.Certificate()
			},
			VerifyConnection: func(cs tls.ConnectionState) error {
				roots, err := creds.Roots()
				if err != nil {
					return err
				}
				return verifyIdentity(cs.PeerCertificates, roots, p.Identity)
			},
		}, nil
	}
	cert, err := tls.LoadX509KeyPair(p.Cert, p.Key)
	if err != nil {
		return nil, err
	}
	roots, err := loadPool(p.CA)
	if err != nil {
		return nil, err
	}
	return dialTLS(cert, roots, p.Identity), nil
}

func (m *Static) Version() uint64 {
	if m == nil {
		return 0
	}
	return m.version
}

func (m *Static) Nodes() []string {
	if m == nil {
		return nil
	}
	return slices.Clone(m.nodes)
}

func (m *Static) Member(node string) (*Member, bool) {
	if m == nil {
		return nil, false
	}
	p, ok := m.byNode[node]
	return p, ok
}

func (m *Static) NodeOf(cert *x509.Certificate) (string, bool) {
	if m == nil || cert == nil {
		return "", false
	}
	for _, u := range cert.URIs {
		if node, ok := m.byIdentity[u.String()]; ok {
			return node, true
		}
	}
	return "", false
}

func ServerTLS(door config.TLSDoor, creds *Credentials) (*tls.Config, error) {
	if door.Cert == "" {
		if creds == nil {
			return nil, errors.New("door.tls: no certificate files and no troupe keys")
		}
		return &tls.Config{
			MinVersion: tls.VersionTLS13,
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				cert, err := creds.Certificate()
				if err != nil {
					return nil, err
				}
				roots, err := creds.Roots()
				if err != nil {
					return nil, err
				}
				return &tls.Config{
					MinVersion:   tls.VersionTLS13,
					Certificates: []tls.Certificate{*cert},
					ClientAuth:   tls.RequireAndVerifyClientCert,
					ClientCAs:    roots,
				}, nil
			},
		}, nil
	}
	cert, err := tls.LoadX509KeyPair(door.Cert, door.Key)
	if err != nil {
		return nil, fmt.Errorf("door.tls: %w", err)
	}
	cas, err := loadPool(door.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("door.tls: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    cas,
	}, nil
}

func dialTLS(cert tls.Certificate, roots *x509.CertPool, identity string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyIdentity(cs.PeerCertificates, roots, identity)
		},
	}
}

func verifyIdentity(chain []*x509.Certificate, roots *x509.CertPool, identity string) error {
	if len(chain) == 0 {
		return errors.New("the peer presented no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	if _, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return err
	}
	for _, u := range chain[0].URIs {
		if u.String() == identity {
			return nil
		}
	}
	return fmt.Errorf("the peer's certificate does not carry %s", identity)
}

func loadPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return pool, nil
}
