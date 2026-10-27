package angelus

import (
	"slices"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

type storeFacts struct{ backend store.Backend }

func StoreFacts(b store.Backend) authz.Facts { return storeFacts{backend: b} }

func (f storeFacts) Owner(aria string) (jkauthz.Principal, bool) {
	minted := ""
	if m, err := f.backend.Meta(aria); err == nil && m != nil {
		minted = m.MintedBy
	} else if t, ok := f.tombstone(aria); ok {
		minted = t.Facts.MintedBy
	}
	if minted == "" {
		return jkauthz.Principal{}, false
	}
	p, err := jkauthz.ParsePrincipal(minted)
	return p, err == nil
}

func (f storeFacts) tombstone(aria string) (store.Tombstone, bool) {
	m, ok := f.backend.(interface{ Moves() *store.Moves })
	if !ok || m.Moves() == nil {
		return store.Tombstone{}, false
	}
	return m.Moves().Tombstone(aria)
}

func (f storeFacts) DescendsFrom(aria, root string) bool {
	if _, ok := f.backend.Node(aria); !ok {
		if t, ok := f.tombstone(aria); ok {
			return slices.Contains(t.Facts.Ancestors, root)
		}
		return false
	}
	seen := map[string]bool{}
	for id := aria; id != "" && !seen[id]; {
		seen[id] = true
		n, ok := f.backend.Node(id)
		if !ok {
			return false
		}
		if n.Parent == root {
			return true
		}
		id = n.Parent
	}
	return false
}

func (f storeFacts) RoleTarget(role string) string {
	snap, err := f.backend.FormState(role)
	if err != nil {
		return ""
	}
	if s := snap.Lookup("target-aria"); s != nil {
		return *s
	}
	return ""
}

func (f storeFacts) SandboxOf(aria string) string {
	snap, err := f.backend.FormState(aria)
	if err != nil {
		return ""
	}
	if s := snap.Lookup("system.sandbox"); s != nil {
		return *s
	}
	return ""
}
