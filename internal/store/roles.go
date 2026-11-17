package store

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/message"
)

const (
	targetAriaKey = "target-aria"
	nameKey       = "name"
	sandboxKey    = "system.sandbox"
)

type roleIndex struct {
	byForm map[string]string
	names  map[string]string
	boxes  map[string]string
	ready  bool
}

type FormListing struct {
	Name, Target, Sandbox string
}

func targetOf(patch message.Patch) (target string, spoke bool) {
	return stringKeyOf(patch, targetAriaKey)
}

func (b *XwalBackend) roleNoted(formID, target string) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	if b.roles.byForm == nil {
		b.roles.byForm = map[string]string{}
	}
	if target == "" {
		delete(b.roles.byForm, formID)
		return
	}
	b.roles.byForm[formID] = target
}

func (b *XwalBackend) nameNoted(formID, name string) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	noted(&b.roles.names, formID, name)
}

func (b *XwalBackend) boxNoted(formID, box string) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	noted(&b.roles.boxes, formID, box)
}

func noted(m *map[string]string, id, v string) {
	if v == "" {
		delete(*m, id)
		return
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[id] = v
}

func (b *XwalBackend) roleCommitted(formID string, patch message.Patch) {
	if target, spoke := targetOf(patch); spoke {
		b.roleNoted(formID, target)
	}
	if name, spoke := stringKeyOf(patch, nameKey); spoke {
		b.nameNoted(formID, name)
	}
	if box, spoke := stringKeyOf(patch, sandboxKey); spoke {
		b.boxNoted(formID, box)
	}
}

func (b *XwalBackend) roleFromState(formID string) {
	snap, err := b.FormState(formID)
	if err != nil {
		return
	}
	target := ""
	if v := snap.Lookup(targetAriaKey); v != nil {
		target = *v
	}
	b.roleNoted(formID, target)
	name := ""
	if v := snap.Lookup(nameKey); v != nil {
		name = *v
	}
	b.nameNoted(formID, name)
	box := ""
	if v := snap.Lookup(sandboxKey); v != nil {
		box = *v
	}
	b.boxNoted(formID, box)
}

func (b *XwalBackend) rolesForgotten(ids []string) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	for _, id := range ids {
		delete(b.roles.byForm, id)
		delete(b.roles.names, id)
		delete(b.roles.boxes, id)
	}
}

func (b *XwalBackend) FormsNamed(name string) (roles, forms []string, ready bool) {
	b.rolesMu.Lock()
	var plain []string
	for formID, n := range b.roles.names {
		if n != name {
			continue
		}
		if b.roles.byForm[formID] != "" {
			roles = append(roles, formID)
		} else {
			plain = append(plain, formID)
		}
	}
	ready = b.roles.ready
	b.rolesMu.Unlock()
	for _, id := range plain {
		if kind, ok := b.store.trunks.Kind(id); ok && kind == string(kindForm) {
			forms = append(forms, id)
		}
	}
	sort.Strings(roles)
	sort.Strings(forms)
	return roles, forms, ready
}

func (b *XwalBackend) FormListing(formID string) (l FormListing, ready bool) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	return FormListing{
		Name:    b.roles.names[formID],
		Target:  b.roles.byForm[formID],
		Sandbox: b.roles.boxes[formID],
	}, b.roles.ready
}

func (b *XwalBackend) RoleForms() (map[string]string, bool) {
	b.rolesMu.Lock()
	defer b.rolesMu.Unlock()
	out := make(map[string]string, len(b.roles.byForm))
	for formID, target := range b.roles.byForm {
		out[formID] = target
	}
	return out, b.roles.ready
}

func (b *XwalBackend) SeedRoles(ctx context.Context, pace time.Duration) {
	start := time.Now()
	forms := b.Forms()
	for _, f := range forms {
		if ctx.Err() != nil {
			return
		}
		b.roleFromState(f.ID)
		if pace > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pace):
			}
		}
	}
	b.rolesMu.Lock()
	b.roles.ready = true
	n := len(b.roles.byForm)
	b.rolesMu.Unlock()
	slog.Info("role index seeded", "forms", len(forms), "roles", n, "took", time.Since(start).Round(time.Millisecond))
}
