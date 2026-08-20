package store

import (
	"os"
	"testing"
)

// Does a real translator channel hold MORE THAN ONE row per fig IR record?
// A re-encode under a changed fingerprint appends a second row at the same
// foreign key, and today's read never noticed because the projection kept
// one entry per record in memory. Reading the log directly would send both.
func TestTranslationRowsPerRecord(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	var arias, rows, dupRecords, maxPer int
	fps := map[string]int{}
	for _, n := range be.Nodes() {
		log, err := be.OpenTranslator(n.ID, "anthropic")
		if err != nil {
			continue
		}
		entries := log.Read()
		if len(entries) == 0 {
			continue
		}
		arias++
		per := map[uint64]int{}
		for _, e := range entries {
			rows++
			per[e.FigaroLT]++
			fps[e.Fingerprint]++
		}
		for _, c := range per {
			if c > 1 {
				dupRecords++
			}
			if c > maxPer {
				maxPer = c
			}
		}
	}
	t.Logf("arias=%d rows=%d records with MORE THAN ONE row=%d (max rows for one record=%d)", arias, rows, dupRecords, maxPer)
	t.Logf("distinct fingerprints across all rows: %d", len(fps))
	for fp, n := range fps {
		if len(fps) <= 8 {
			t.Logf("   fingerprint %-40q %d rows", fp, n)
		}
	}
}
