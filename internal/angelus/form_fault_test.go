package angelus

import (
	"errors"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

type FormFault struct {
	*store.XwalBackend
	mu  sync.Mutex
	ids map[string]bool
}

var errFormFault = errors.New("form unreadable (test fault)")

func NewFormFault(b *store.XwalBackend) *FormFault {
	return &FormFault{XwalBackend: b, ids: map[string]bool{}}
}

func (f *FormFault) Break(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids[id] = true
}

func (f *FormFault) Heal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.ids)
}

func (f *FormFault) FormState(id string) (form.Snapshot, error) {
	f.mu.Lock()
	broken := f.ids[id]
	f.mu.Unlock()
	if broken {
		return form.Snapshot{}, errFormFault
	}
	return f.XwalBackend.FormState(id)
}

// ReadsScopesThrough rebuilds the angelus's policies so every scope read goes
// through f; the store itself stays the one the handlers hold.
func ReadsScopesThrough(f *FormFault) func(*ServerConfig) {
	return func(cfg *ServerConfig) {
		a := cfg.Angelus
		f.XwalBackend = a.Backend.(*store.XwalBackend)
		p := authz.NewPolicies(a.Peercred.Self(), StoreFacts(f))
		p.SetBoxes(a.Settings.BoxesWithEndpoints())
		p.SetBoxesRequiringClaim(a.Settings.BoxesRequiringClaim())
		if err := p.Reload(a.Settings.PolicyPath()); err != nil {
			panic(err)
		}
		a.Policies = p
	}
}

func ReadsFormsThrough(f *FormFault) func(*ServerConfig) {
	return func(cfg *ServerConfig) {
		f.XwalBackend = cfg.Angelus.Backend.(*store.XwalBackend)
		cfg.Angelus.Backend = f
	}
}
