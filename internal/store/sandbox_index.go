package store

import (
	"sync"

	"github.com/jack-work/figaro/api/message"
)

const sandboxKey = "system.sandbox"

// sandboxIndex remembers each node's system.sandbox once read, across form
// eviction. Authz asks it for every row a list returns (the boxed, unboxed and
// boxed-in selectors); answered from the form alone, each of those asks
// re-opened and replayed a form the idle sweep had dropped (#130).
//
// An entry is filled from a form read or from a commit that reaches the key.
// Every form write commits through Form.OnCommit (formCommitted), so a filled
// entry cannot go stale. The version guards the race of a fill that read
// before a commit landing after it.
type sandboxIndex struct {
	mu   sync.Mutex
	held map[string]sandboxEntry
}

type sandboxEntry struct {
	box     string
	version uint64
}

func (x *sandboxIndex) get(id string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	e, ok := x.held[id]
	return e.box, ok
}

func (x *sandboxIndex) note(id, box string, version uint64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.held == nil {
		x.held = map[string]sandboxEntry{}
	}
	if cur, ok := x.held[id]; ok && cur.version > version {
		return
	}
	x.held[id] = sandboxEntry{box: box, version: version}
}

func (x *sandboxIndex) forget(ids []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, id := range ids {
		delete(x.held, id)
	}
}

// HeldSandbox is the node's system.sandbox ("" when unset) and whether its
// form could be read. A miss reads the form once; later asks are a map hit.
func (b *XwalBackend) HeldSandbox(id string) (string, bool) {
	if box, ok := b.sandboxes.get(id); ok {
		return box, true
	}
	at, err := b.FormAt(id)
	if err != nil {
		return "", false
	}
	box := ""
	if v := at.Snapshot.Lookup(sandboxKey); v != nil {
		box = *v
	}
	b.sandboxes.note(id, box, at.Version)
	return box, true
}

func (b *XwalBackend) sandboxCommitted(id string, patch message.Patch, f *Form) {
	if !patchReaches(patch, sandboxKey) {
		return
	}
	at := f.Read()
	box := ""
	if v := at.Snapshot.Lookup(sandboxKey); v != nil {
		box = *v
	}
	b.sandboxes.note(id, box, at.Version)
}

// FormLabels answers a form's name and target-aria from the role index, so a
// listing need not open the form. ok is false until the index is seeded.
func (b *XwalBackend) FormLabels(id string) (name, target string, ok bool) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	if !b.roles.ready {
		return "", "", false
	}
	return b.roles.names[id], b.roles.byForm[id], true
}
