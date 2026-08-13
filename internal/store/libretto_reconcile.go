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
	"log/slog"
	"strings"
)

// LibrettoAudit is what one reconciliation pass found.
type LibrettoAudit struct {
	Boards    int // boards read
	Librettos int // librettos examined
	Corrected int // refcounts that were wrong
	Orphaned  int // librettos no board names at all
	Missing   int // studied forms STILL without a libretto when the pass ended
	Minted    int // librettos this pass created (the migration)
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

// HasLibrettos reports whether this store holds any at all. It is a stump
// name scan and touches no boards, which is what lets the boot sweep cost
// nothing on a store that has never studied anything -- which is every store
// until the verb is used.
func (b *XwalBackend) HasLibrettos() bool {
	for _, st := range b.store.trunks.Stumps() {
		if _, ok := SourceOfLibretto(st.Name); ok {
			return true
		}
	}
	return false
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
		// The SHARED instance, never a fresh one: opening a second Libretto
		// over the same stump puts a second writer on its channel.
		lib, err := b.librettoInstance(source)
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
					return audit, fmt.Errorf("reconcile %s: %w", st.Name, err)
				}
			}
			audit.Corrected++
		}
	}
	for source := range want {
		if seen[source] {
			continue
		}
		// A form somebody studies with no libretto to hold the copy. This is
		// the MIGRATION case and it is not hypothetical: the author's store
		// has eleven such studies, all made before librettos existed. Left
		// alone they would never acquire one, because the study verb mints
		// at study time and these were studied long ago.
		audit.Missing++
		if !apply {
			continue
		}
		// Minting seeds the copy from the source and starts the fold; the
		// refcount is then whatever the boards say, which is what the rest
		// of this pass already computes.
		lib, err := b.libretto(source)
		if err != nil {
			slog.Info("reconcile: cannot mint a libretto for a studied form",
				"form", source, "err", err)
			continue // and it is still missing, which is what Missing means
		}
		// Repaired, so it is no longer missing. Counting the PRE-state here
		// made the pass report "missing 4" immediately after creating those
		// four, which is a repair tool lying about its own work.
		audit.Missing--
		audit.Minted++
		slog.Info("reconcile: minted a libretto for a pre-existing study",
			"libretto", lib.ID(), "source", source, "refs", want[source])
		if lib.Refs() != want[source] {
			if err := lib.setRefs(want[source]); err != nil {
				return audit, fmt.Errorf("reconcile mint %s: %w", source, err)
			}
			audit.Corrected++
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
