package store

// Reconciliation: recompute every libretto's refcount from the boards that
// name it (durable-forms §12.2.1, §12.2.2).
//
// The authoritative fact is "figaro X studies form Y", and it lives in X's
// board as `system.studies`. The refcount on the libretto is a DERIVED
// number, kept incrementally by the study verb because reclamation needs to
// answer "is anyone still reading this" without a scan.
//
// Incremental counts drift. The study path is ordered so a crash always
// leaves the count too HIGH (a leak, recoverable), but three write sites
// outside that path break it in the unrecoverable direction: a FORK gives a
// child its parent's study-set with nothing incrementing the librettos it
// names, an IMPORT restores a board wholesale, and a KILL removes one
// without decrementing.
//
// So this RECOMPUTES rather than adjusts, which is what makes it a backstop
// for both directions rather than for the safe one only. It runs at daemon
// start or on demand; it narrows the window rather than closing it, and the
// orderings in the write paths are still required.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// LibrettoAudit is what one reconciliation pass found.
type LibrettoAudit struct {
	Boards    int // boards read
	Librettos int // librettos examined
	Corrected int // refcounts that were wrong
	Orphaned  int // librettos no board names at all
	Missing   int // studied forms with no libretto yet
}

// StudiesKey is the board key naming the forms a figaro studies. It is
// figaro's constant; the store needs it to reconcile and must not own a
// second copy of the name.
const StudiesKey = "system.studies"

// ReconcileLibrettos recounts every libretto from the boards. Idempotent, and
// safe to run while the daemon is live: each correction is an ordinary patch
// on the libretto's own actor, so a study landing mid-sweep either lands
// before the recount (and is counted) or after it (and moves the count from
// the value the sweep just wrote).
func (b *XwalBackend) ReconcileLibrettos() (LibrettoAudit, error) {
	return b.reconcileLibrettos(true)
}

// AuditLibrettos is ReconcileLibrettos without the writes: the same counts,
// nothing changed. `Corrected` then reads as "would correct".
func (b *XwalBackend) AuditLibrettos() (LibrettoAudit, error) {
	return b.reconcileLibrettos(false)
}

func (b *XwalBackend) reconcileLibrettos(apply bool) (LibrettoAudit, error) {
	var audit LibrettoAudit

	// What the boards actually say.
	want := map[string]int{}
	for _, n := range b.store.Nodes() {
		if isReservedStump(n.ID) {
			continue
		}
		snap, err := b.FormState(n.ID)
		if err != nil {
			continue // a node whose board cannot be read cannot claim a study
		}
		audit.Boards++
		for _, formID := range studiesOf(snap) {
			want[strings.TrimPrefix(formID, "@")]++
		}
	}

	// What the librettos believe.
	seen := map[string]bool{}
	for _, st := range b.store.trunks.Stumps() {
		source, ok := SourceOfLibretto(st.Name)
		if !ok {
			continue
		}
		seen[source] = true
		audit.Librettos++
		lib, err := OpenLibretto(b.store, source)
		if err != nil {
			return audit, fmt.Errorf("reconcile %s: %w", st.Name, err)
		}
		n := want[source]
		if n == 0 {
			audit.Orphaned++
		}
		if lib.Refs() != n {
			if apply {
				if err := lib.setRefs(n); err != nil {
					lib.Close()
					return audit, fmt.Errorf("reconcile %s: %w", st.Name, err)
				}
			}
			audit.Corrected++
		}
		lib.Close()
	}
	for source := range want {
		if !seen[source] {
			// A form somebody studies with no libretto to hold the copy.
			// Reported, not created: minting one is the study verb's job and
			// it needs the source form to seed from.
			audit.Missing++
		}
	}
	return audit, nil
}

// setRefs writes the recomputed count. Privileged and unconditional: the
// sweep's whole point is that it knows better than the incremental value it
// is replacing.
func (l *Libretto) setRefs(n int) error {
	_, _, err := l.form.ApplyEffectPrivileged(
		librettoPatch(map[string]any{KeyLibrettoRefs: n}), 0)
	return err
}

// studiesOf reads a board's declared study set.
func studiesOf(snap interface {
	Get(string) (json.RawMessage, bool)
}) []string {
	raw, ok := snap.Get(StudiesKey)
	if !ok {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return ids
}

// isReservedStump reports the nodes that carry machinery rather than a
// conversation: the topology form and every libretto.
func isReservedStump(id string) bool {
	if id == topologyStump {
		return true
	}
	_, isLibretto := SourceOfLibretto(id)
	return isLibretto
}
