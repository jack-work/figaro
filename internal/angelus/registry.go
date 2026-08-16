// Package angelus implements the figaro supervisor.
package angelus

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
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

	killing map[string]bool
	// retiring mirrors killing for hibernation. Separate because the two
	// differ in what survives: killing deletes an aria, retiring only
	// reclaims its agent.
	retiring map[string]bool

	draining atomic.Bool
}

// NewRegistry creates an empty registry.
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

// Register adds a figaro to the registry. It must not touch figaroPIDs:
// Bind is that map's only writer, and it allocates lazily.
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

// Get returns a figaro by ID, or nil if not found.
func (r *Registry) Get(id string) figaro.Figaro {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.figaros[id]
}

// Kill removes a figaro, unbinds its PIDs, and kills it. The id stays
// registered until the kill (which waits out the drain loop's final seal
// appends) completes: unpublishing first would let a concurrent restore
// run tail repair against the still-sealing agent.
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
		// Also the pending fork point. Unobservable through Resolve, which
		// gates pidToLT behind pidToFigaro, so this is a leak rather than a
		// wrong answer -- and the only map a killed aria used to leave behind.
		delete(r.pidToLT, pid)
	}
	delete(r.figaroPIDs, id)
	delete(r.figaros, id)
	delete(r.killing, id)
	r.mu.Unlock()
	return nil
}

// Hibernate reclaims an aria's agent while KEEPING everything that makes the
// aria addressable: its pid bindings, its trunk, its endpoint. It is Kill
// minus the deletion.
//
// The differences from Kill are the whole feature, so they are spelled out:
// bindings survive (a bound shell's next bare prompt must land on the same
// trunk, not mint a new aria), figaroPIDs survives (the sweep must not
// silently detach a terminal), and the caller does NOT drop the hub: the
// endpoint outliving the agent is the point.
//
// Refuses an aria with a turn in flight, and re-checks that immediately
// before teardown: a prompt can arrive between the sweep's decision and this
// call, and losing that race must cost a skipped reclamation, never a
// dropped prompt. The id stays published until teardown completes, exactly
// as Kill does, so a concurrent restore cannot build a second agent against
// a still-sealing log.
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

	// Last look before the irreversible part. A turn that opened while we
	// were taking the flag wins.
	if f.Info().State != "idle" {
		r.mu.Lock()
		delete(r.retiring, id)
		r.mu.Unlock()
		return fmt.Errorf("figaro %q became active", id)
	}

	f.Kill() // tears the agent down; runs OnTeardown, which unbinds the hub

	r.mu.Lock()
	delete(r.figaros, id)
	delete(r.retiring, id)
	r.mu.Unlock()
	return nil
}

// Retiring reports whether an aria is mid-hibernate. A restore must wait
// rather than race it.
func (r *Registry) Retiring(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.retiring[id]
}

// Bind maps a pid to a figaro. Unbinds from any previous figaro.
// Bind binds pid to figaroID with an optional pending fork-point lt (0 = the
// trunk's leaf). lt is always (re)set, so a plain rebind clears any prior
// pending LT.
// A binding names WHICH ARIA a shell is attended to. That is an identity
// fact, not a memory fact, so it deliberately does NOT require the aria to
// be resident: binding a dormant aria is legal and does not wake it. This is
// what makes `figaro attend` free, and what lets a bound shell keep its
// attendance across a hibernate: without it, the sweep would silently
// detach every terminal it reclaimed.
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

// Resolve returns the aria a pid is attended to. The figaro is nil when the
// aria is dormant, which is now an ordinary answer rather than a miss: the
// caller has the id, and the id is all an endpoint address needs.
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

// TurnDonor offers a newly opened aria the composed turns its nearest LIVE
// ancestor already holds below the child's fork base.
//
// Phase 4, measured by identity: two branches of one trunk compose the same
// history into separate node structs, and the minted strings are the tool
// nodes' Input and Output, which dominate by bytes. The agent's splice
// (internal/figaro/seed_turns.go) refuses any donation it cannot prove is a
// prefix of its own log, so the worst case here is a wasted lookup.
//
// Nil is the common answer and always legal: no ancestry, no live ancestor, or
// an ancestor that has materialized nothing yet.
func (a *Angelus) TurnDonor(childID string) []aria.Turn {
	lb, ok := a.Backend.(store.LineageBackend)
	if !ok || a.Registry == nil {
		return nil
	}
	refs := lb.Lineage(childID)
	if len(refs) < 2 {
		return nil // a root composes its own history; there is nothing above it
	}
	base := refs[len(refs)-1].Base
	if base == 0 {
		return nil
	}
	// Nearest ancestor first: it holds the longest shared prefix.
	for i := len(refs) - 2; i >= 0; i-- {
		f := a.Registry.Get(refs[i].Node)
		if f == nil {
			continue
		}
		donor, ok := f.(interface{ TurnsBelow(uint64) []aria.Turn })
		if !ok {
			continue
		}
		if turns := donor.TurnsBelow(base); len(turns) > 0 {
			return turns
		}
	}
	return nil
}
