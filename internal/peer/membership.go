package peer

import "crypto/x509"

type Membership interface {
	Nodes() []string
	Member(node string) (*Member, bool)
	NodeOf(cert *x509.Certificate) (string, bool)
}
