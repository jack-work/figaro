package peer

import "crypto/x509"

type Membership interface {
	Version() uint64
	PolicyVersion() uint64
	Nodes() []string
	Member(node string) (*Member, bool)
	NodeOf(cert *x509.Certificate) (string, bool)
}
