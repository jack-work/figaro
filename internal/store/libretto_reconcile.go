package store

// Reconciliation: recompute every libretto's refcount from the boards that
// name it (durable-forms §12.2.1, §12.2.2).

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
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

// StudiesKey is the board key naming the forms a figaro studies. The store
// owns the name -- its reconciliation sweep recomputes refcounts FROM the
// boards -- and figaro re-exports it, because figaro imports the store and
// not the other way around.
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

// RefsNeedMigration reports whether any libretto still carries a COUNT where
// the backref set belongs. Reads only the librettos, never the boards, so a
// migrated store answers in O(librettos) and never scans again.
func (b *XwalBackend) RefsNeedMigration() bool {
	for _, st := range b.store.trunks.Stumps() {
		source, ok := SourceOfLibretto(st.Name)
		if !ok {
			continue
		}
		lib, err := b.librettoInstance(source)
		if err != nil {
			continue
		}
		if !lib.RefsMigrated() {
			return true
		}
	}
	return false
}

func (b *XwalBackend) reconcileLibrettos(apply bool) (LibrettoAudit, error) {
	var audit LibrettoAudit

	// What the boards actually say.
	want := map[string][]string{}
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
			src := strings.TrimPrefix(formID, "@")
			want[src] = append(want[src], n.ID)
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
		if len(n) == 0 {
			audit.Orphaned++
		}
		if !lib.RefsMigrated() || !slices.Equal(lib.RefSet(), sortedIDs(n)) {
			if apply {
				if err := lib.SetRefs(n); err != nil {
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
			"libretto", lib.ID(), "source", source, "refs", len(want[source]))
		if !slices.Equal(lib.RefSet(), sortedIDs(want[source])) {
			if err := lib.SetRefs(want[source]); err != nil {
				return audit, fmt.Errorf("reconcile mint %s: %w", source, err)
			}
			audit.Corrected++
		}
	}
	return audit, nil
}

// sortedIDs is the durable order of a backref set.
func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	for i := range out {
		out[i] = strings.TrimPrefix(out[i], "@")
	}
	slices.Sort(out)
	return slices.Compact(out)
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
