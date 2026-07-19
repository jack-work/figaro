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

	// pidToLT is a per-pid pending fork-point (figaro main-LT). 0 = none:
	// the bound aria's leaf. Set by `attend <id>:<LT>`; consumed by the next
	// prompt (which forks there and rebinds to the new branch, clearing it).
	pidToLT map[int]uint64

	figaroPIDs map[string]map[int]struct{}

	draining atomic.Bool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		figaros:     make(map[string]figaro.Figaro),
		pidToFigaro: make(map[int]string),
		pidToLT:     make(map[int]uint64),
		figaroPIDs:  make(map[string]map[int]struct{}),
	}
}

// Register adds a figaro to the registry.
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
	r.figaroPIDs[f.ID()] = make(map[int]struct{})
	return nil
}

// Get returns a figaro by ID, or nil if not found.
func (r *Registry) Get(id string) figaro.Figaro {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.figaros[id]
}

// Kill removes a figaro, unbinds its PIDs, and kills it.
func (r *Registry) Kill(id string) error {
	r.mu.Lock()
	f, exists := r.figaros[id]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("figaro %q not found", id)
	}

	for pid := range r.figaroPIDs[id] {
		delete(r.pidToFigaro, pid)
	}
	delete(r.figaroPIDs, id)
	delete(r.figaros, id)
	r.mu.Unlock()

	f.Kill()
	return nil
}

// Bind maps a pid to a figaro. Unbinds from any previous figaro.
// Bind binds pid to figaroID with an optional pending fork-point lt (0 = the
// trunk's leaf). lt is always (re)set, so a plain rebind clears any prior
// pending LT.
func (r *Registry) Bind(pid int, figaroID string, lt uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.figaros[figaroID]; !exists {
		return fmt.Errorf("figaro %q not found", figaroID)
	}

	if existing, ok := r.pidToFigaro[pid]; ok && existing == figaroID {
		r.pidToLT[pid] = lt
		return nil
	}

	r.unbindLocked(pid)

	r.pidToFigaro[pid] = figaroID
	r.pidToLT[pid] = lt
	r.figaroPIDs[figaroID][pid] = struct{}{}
	return nil
}

// Resolve returns the figaro for a pid.
func (r *Registry) Resolve(pid int) (string, figaro.Figaro, uint64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id, ok := r.pidToFigaro[pid]
	if !ok {
		return "", nil, 0
	}
	return id, r.figaros[id], r.pidToLT[pid]
}

// Unbind removes a pid binding.
func (r *Registry) Unbind(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unbindLocked(pid)
}

// unbindLocked removes a pid binding. Caller must hold r.mu.
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

// BoundPIDs returns the PIDs bound to a figaro.
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

// List returns info for all registered figaros.
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

// AllPIDs returns all bound PIDs (for the monitor to poll).
func (r *Registry) AllPIDs() []int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]int, 0, len(r.pidToFigaro))
	for pid := range r.pidToFigaro {
		result = append(result, pid)
	}
	return result
}

// FigaroCount returns the number of registered figaros.
func (r *Registry) FigaroCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.figaros)
}

// SetDraining marks the registry as shutting down.
func (r *Registry) SetDraining() {
	r.draining.Store(true)
}

// IsDraining reports whether the registry is in shutdown mode.
func (r *Registry) IsDraining() bool {
	return r.draining.Load()
}

// All returns a snapshot of all registered figaros.
func (r *Registry) All() []figaro.Figaro {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]figaro.Figaro, 0, len(r.figaros))
	for _, f := range r.figaros {
		out = append(out, f)
	}
	return out
}

// BoundPIDCount returns the total number of pid bindings.
func (r *Registry) BoundPIDCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pidToFigaro)
}
