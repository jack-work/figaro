package peer

import (
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/jack-work/figaro/internal/troupe"
)

type Ledger struct {
	self   string
	troupe string
	creds  *Credentials

	mu      sync.RWMutex
	ledger  *troupe.Ledger
	view    troupe.View
	members map[string]*Member
	nodes   []string
	evict   []func(node string)
}

func NewLedger(self string, l *troupe.Ledger, creds *Credentials) *Ledger {
	m := &Ledger{self: self, troupe: l.Troupe(), ledger: l}
	m.creds = &Credentials{Certificate: creds.Certificate, Roots: creds.Roots, Admit: m.Admit}
	m.rebuild()
	return m
}

func (m *Ledger) Credentials() *Credentials { return m.creds }

func (m *Ledger) OnEvict(fn func(node string)) {
	m.mu.Lock()
	m.evict = append(m.evict, fn)
	m.mu.Unlock()
}

func (m *Ledger) Merge(es ...troupe.Entry) ([]troupe.Entry, error) {
	m.mu.Lock()
	before := m.members
	added, err := m.ledger.Merge(es...)
	if len(added) > 0 {
		m.rebuild()
	}
	var gone []string
	for node := range before {
		if _, ok := m.members[node]; !ok {
			gone = append(gone, node)
		}
	}
	evict := slices.Clone(m.evict)
	m.mu.Unlock()
	slices.Sort(gone)
	for _, node := range gone {
		for _, fn := range evict {
			fn(node)
		}
	}
	return added, err
}

func (m *Ledger) rebuild() {
	m.view = m.ledger.View()
	members := make(map[string]*Member, len(m.view.Members))
	for node, mem := range m.view.Members {
		if node == m.self {
			continue
		}
		id := troupe.Identity(m.troupe, node)
		if old, ok := m.members[node]; ok && old.Address == mem.Door {
			members[node] = old
			continue
		}
		members[node] = &Member{Node: node, Address: mem.Door, Identity: id, tls: keyedTLS(id, m.creds)}
	}
	m.members = members
	m.nodes = slices.Sorted(maps.Keys(members))
}

func (m *Ledger) Entries() []troupe.Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ledger.Entries()
}

func (m *Ledger) View() troupe.View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.view
}

func (m *Ledger) Version() uint64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.view.Digest
}

func (m *Ledger) Nodes() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.nodes)
}

func (m *Ledger) Member(node string) (*Member, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.members[node]
	return p, ok
}

func (m *Ledger) NodeOf(cert *x509.Certificate) (string, bool) {
	if m == nil || cert == nil {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range cert.URIs {
		if node, ok := troupe.NodeOf(m.troupe, u); ok {
			if _, member := m.members[node]; member {
				return node, true
			}
		}
	}
	return "", false
}

func (m *Ledger) Admit(chain []*x509.Certificate) error {
	if len(chain) == 0 {
		return errors.New("the peer presented no certificate")
	}
	node, ok := m.NodeOf(chain[0])
	if !ok {
		return fmt.Errorf("the certificate names no member of troupe %s", m.troupe)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range chain[1:] {
		if !c.IsCA {
			continue
		}
		if by := troupe.IntermediateNode(c); m.view.Gone[by] == troupe.Revoke {
			return fmt.Errorf("node %s's certificate was issued by %s, whose keys the root revoked", node, by)
		}
	}
	return nil
}

var _ Membership = (*Ledger)(nil)
