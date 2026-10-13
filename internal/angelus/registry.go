// Package angelus implements the figaro supervisor.
package angelus

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/figaro"
)

// Registry holds running figaros and the pid->figaro index (1:1).
type Registry struct {
	mu sync.RWMutex

	figaros map[string]figaro.Figaro

	pidToFigaro map[int]string

	// pidToLT is a pending fork point per pid, 0 = the bound aria's leaf;
	// the next prompt forks there and clears it.
	pidToLT map[int]uint64

	figaroPIDs map[string]map[int]struct{}

	killing  map[string]bool
	retiring map[string]bool

	draining atomic.Bool
}

func NewRegistry() *Registry {
	return &Registry{
		figaros:     make(map[string]figaro.Figaro),
		pidToFigaro: make(map[int]string),
		pidToLT:     make(map[int]uint64),
		figaroPIDs:  make(map[string]map[int]struct{}),
		killing:     make(map[string]bool),
		retiring:    make(map[string]bool),
	}
}

// Register must not touch figaroPIDs: Bind is that map's only writer.
func (r *Registry) Register(f figaro.Figaro) error {
	if r.draining.Load() {
		return fmt.Errorf("angelus: shutting down, refusing new figaros")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.figaros[f.ID()]; exists {
		return fmt.Errorf("figaro %q already registered", f.ID())
	}
	r.figaros[f.ID()] = f
	return nil
}

func (r *Registry) Get(id string) figaro.Figaro {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.figaros[id]
}

// Kill keeps the id registered until the kill completes: unpublishing first
// would let a concurrent restore run tail repair against the still-sealing agent.
func (r *Registry) Kill(id string) error {
	r.mu.Lock()
	f, exists := r.figaros[id]
	if !exists || r.killing[id] {
		r.mu.Unlock()
		return fmt.Errorf("figaro %q not found", id)
	}
	r.killing[id] = true
	r.mu.Unlock()

	f.Kill()

	r.mu.Lock()
	for pid := range r.figaroPIDs[id] {
		delete(r.pidToFigaro, pid)
		delete(r.pidToLT, pid)
	}
	delete(r.figaroPIDs, id)
	delete(r.figaros, id)
	delete(r.killing, id)
	r.mu.Unlock()
	return nil
}

// Hibernate is Kill minus the deletion: pid bindings, trunk and endpoint stay.
func (r *Registry) Hibernate(id string) error {
	r.mu.Lock()
	f, exists := r.figaros[id]
	if !exists || r.killing[id] || r.retiring[id] {
		r.mu.Unlock()
		return fmt.Errorf("figaro %q not reclaimable", id)
	}
	if f.Info().State != "idle" {
		r.mu.Unlock()
		return fmt.Errorf("figaro %q is active", id)
	}
	r.retiring[id] = true
	r.mu.Unlock()

	// A turn that opened while the flag was being taken wins.
	if f.Info().State != "idle" {
		r.mu.Lock()
		delete(r.retiring, id)
		r.mu.Unlock()
		return fmt.Errorf("figaro %q became active", id)
	}

	f.Kill()

	r.mu.Lock()
	delete(r.figaros, id)
	delete(r.retiring, id)
	r.mu.Unlock()
	return nil
}

// Retiring reports a hibernate in progress; a restore must wait rather than race it.
func (r *Registry) Retiring(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.retiring[id]
}

// Bind rebinds from any previous figaro and always resets lt. It does not
// require the aria to be resident: binding a dormant aria never wakes it.
func (r *Registry) Bind(pid int, figaroID string, lt uint64) error {
	if figaroID == "" {
		return fmt.Errorf("bind: empty figaro id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.pidToFigaro[pid]; ok && existing == figaroID {
		r.pidToLT[pid] = lt
		return nil
	}

	r.unbindLocked(pid)

	r.pidToFigaro[pid] = figaroID
	r.pidToLT[pid] = lt
	if r.figaroPIDs[figaroID] == nil {
		r.figaroPIDs[figaroID] = map[int]struct{}{}
	}
	r.figaroPIDs[figaroID][pid] = struct{}{}
	return nil
}

// Resolve returns a nil figaro for a dormant aria; the id is still a found answer.
func (r *Registry) Resolve(pid int) (string, figaro.Figaro, uint64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id, ok := r.pidToFigaro[pid]
	if !ok {
		return "", nil, 0
	}
	return id, r.figaros[id], r.pidToLT[pid]
}

func (r *Registry) Unbind(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unbindLocked(pid)
}

func (r *Registry) unbindLocked(pid int) {
	id, ok := r.pidToFigaro[pid]
	if !ok {
		return
	}
	delete(r.pidToFigaro, pid)
	delete(r.pidToLT, pid)
	if pids, exists := r.figaroPIDs[id]; exists {
		delete(pids, pid)
	}
}

func (r *Registry) BoundPIDs(figaroID string) []int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pids, exists := r.figaroPIDs[figaroID]
	if !exists {
		return nil
	}
	result := make([]int, 0, len(pids))
	for pid := range pids {
		result = append(result, pid)
	}
	return result
}

func (r *Registry) List() []figaro.FigaroInfo {
	r.mu.RLock()
	figaros := make([]figaro.Figaro, 0, len(r.figaros))
	for _, f := range r.figaros {
		figaros = append(figaros, f)
	}
	r.mu.RUnlock()

	result := make([]figaro.FigaroInfo, 0, len(figaros))
	for _, f := range figaros {
		result = append(result, f.Info())
	}
	return result
}

func (r *Registry) BoundPIDsByFigaro() map[string][]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]int, len(r.figaroPIDs))
	for id, pids := range r.figaroPIDs {
		bound := make([]int, 0, len(pids))
		for pid := range pids {
			bound = append(bound, pid)
		}
		out[id] = bound
	}
	return out
}

func (r *Registry) AllPIDs() []int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]int, 0, len(r.pidToFigaro))
	for pid := range r.pidToFigaro {
		result = append(result, pid)
	}
	return result
}

func (r *Registry) FigaroCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.figaros)
}

func (r *Registry) SetDraining() {
	r.draining.Store(true)
}

func (r *Registry) IsDraining() bool {
	return r.draining.Load()
}

func (r *Registry) All() []figaro.Figaro {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]figaro.Figaro, 0, len(r.figaros))
	for _, f := range r.figaros {
		out = append(out, f)
	}
	return out
}

func (r *Registry) BoundPIDCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pidToFigaro)
}
