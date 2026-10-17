package angelus

import (
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

type storeFacts struct{ backend store.Backend }

func StoreFacts(b store.Backend) authz.Facts { return storeFacts{backend: b} }

func (f storeFacts) Owner(aria string) (jkauthz.Principal, bool) {
	m, err := f.backend.Meta(aria)
	if err != nil || m == nil || m.MintedBy == "" {
		return jkauthz.Principal{}, false
	}
	p, err := jkauthz.ParsePrincipal(m.MintedBy)
	return p, err == nil
}

func (f storeFacts) DescendsFrom(aria, root string) bool {
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
