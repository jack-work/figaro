package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

type LibrettoAudit struct {
	BoundForms int // bound forms read
	Librettos  int // librettos examined
	Corrected  int // refcounts that were wrong
	Orphaned   int // librettos no bound form names at all
	Missing    int // studied forms STILL without a libretto when the pass ended
	Minted     int // librettos this pass created (the migration)
}

// StudiesKey: the store owns the name because its reconciliation sweep
// recomputes refcounts from the bound forms; figaro re-exports it.
const StudiesKey = "system.studies"

// ReconcileLibrettos is idempotent and safe while live: each correction is an
// ordinary patch on the libretto's own actor, so a study landing mid-sweep is
// counted either before or after it.
func (b *XwalBackend) ReconcileLibrettos() (LibrettoAudit, error) {
	return b.reconcileLibrettos(true)
}

// AuditLibrettos is ReconcileLibrettos without the writes; Corrected reads as "would correct".
func (b *XwalBackend) AuditLibrettos() (LibrettoAudit, error) {
	return b.reconcileLibrettos(false)
}

// RefsNeedMigration reads only the librettos, never the bound forms.
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

	// What the bound forms actually say.
	want := map[string][]string{}
	for _, n := range b.store.Nodes() {
		if isReservedStump(n.ID) {
			continue
		}
		snap, err := b.FormState(n.ID)
		if err != nil {
			continue // a node whose bound form cannot be read cannot claim a study
		}
		audit.BoundForms++
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
		// The shared instance: a second Libretto over the same stump is a
		// second writer on its channel.
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
		// A studied form with no libretto: a study made before librettos
		// existed, which the study verb (minting at study time) never revisits.
		audit.Missing++
		if !apply {
			continue
		}
		lib, err := b.libretto(source)
		if err != nil {
			slog.Info("reconcile: cannot mint a libretto for a studied form",
				"form", source, "err", err)
			continue // and it is still missing, which is what Missing means
		}
		// Repaired, so no longer missing.
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

func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	for i := range out {
		out[i] = strings.TrimPrefix(out[i], "@")
	}
	slices.Sort(out)
	return slices.Compact(out)
}

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

// isReservedStump: nodes carrying machinery rather than a conversation.
func isReservedStump(id string) bool {
	if id == topologyStump || id == modelsStump {
		return true
	}
	_, isLibretto := SourceOfLibretto(id)
	return isLibretto
}
